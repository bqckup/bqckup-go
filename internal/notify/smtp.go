package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
)

// SMTP sends one branded HTML email per notification. Transport rules:
// port 465 is implicit TLS; any other port uses STARTTLS when the server
// offers it. PLAIN credentials are used only when username/password envs are
// present and the session is encrypted (implicit TLS or STARTTLS); a server
// without STARTTLS fails the channel instead of sending credentials in
// cleartext. Unauthenticated SMTP is allowed.
type SMTP struct {
	name        string
	host        string
	port        int
	usernameEnv string
	passwordEnv string
	from        string
	to          []string
	lookupEnv   func(string) (string, bool)
	// roots overrides the TLS root pool; nil means system roots. Tests
	// inject a pool containing the fake server's self-signed certificate.
	roots *x509.CertPool
	// implicitTLS is true for port 465 (and in tests, for an ephemeral
	// port simulating it).
	implicitTLS bool
}

func NewSMTP(name string, channel config.Channel, lookupEnv func(string) (string, bool), roots *x509.CertPool) *SMTP {
	return &SMTP{
		name:        name,
		host:        channel.Host,
		port:        channel.Port,
		usernameEnv: channel.UsernameEnv,
		passwordEnv: channel.PasswordEnv,
		from:        channel.From,
		to:          channel.To,
		lookupEnv:   lookupEnv,
		roots:       roots,
		implicitTLS: channel.Port == 465,
	}
}

func (s *SMTP) Name() string { return s.name }

func (s *SMTP) Send(ctx context.Context, payload Payload) error {
	address := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	// One deadline bounds the whole session: dial, handshake, auth, and
	// message transfer.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	username, password, err := s.credentials()
	if err != nil {
		return err
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("dial smtp server: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid smtp address: %w", err)
	}
	if s.implicitTLS {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host, RootCAs: s.roots, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("tls handshake: %w", err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("start smtp session: %w", err)
	}
	defer client.Close()

	if !s.implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, RootCAs: s.roots, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("start tls: %w", err)
			}
		} else if username != "" {
			return fmt.Errorf("smtp server does not offer STARTTLS; refusing to send credentials in cleartext")
		}
	}
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			return fmt.Errorf("smtp authentication failed: %w", err)
		}
	}

	subject := fmt.Sprintf("[bqckup] %s: %s", payload.Event, payload.Site)
	message := s.renderMessage(subject, payload)
	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, recipient := range s.to {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("recipient %q: %w", recipient, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start message data: %w", err)
	}
	if _, err := writer.Write([]byte(message)); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish message: %w", err)
	}
	return client.Quit()
}

// credentials resolves the SMTP credentials at send time. A missing or empty
// referenced variable is a per-channel error.
func (s *SMTP) credentials() (username, password string, err error) {
	if s.usernameEnv == "" {
		return "", "", nil
	}
	username, ok := s.lookupEnv(s.usernameEnv)
	if !ok || username == "" {
		return "", "", fmt.Errorf("environment variable %q is not set", s.usernameEnv)
	}
	password, ok = s.lookupEnv(s.passwordEnv)
	if !ok || password == "" {
		return "", "", fmt.Errorf("environment variable %q is not set", s.passwordEnv)
	}
	return username, password, nil
}

func (s *SMTP) renderMessage(subject string, payload Payload) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "From: %s\r\n", s.from)
	fmt.Fprintf(&builder, "To: %s\r\n", strings.Join(s.to, ", "))
	fmt.Fprintf(&builder, "Subject: %s\r\n", subject)
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	builder.WriteString("\r\n")
	builder.WriteString(s.renderHTML(subject, payload))
	return builder.String()
}

// renderHTML builds the minimal branded email body: a status-colored banner,
// a field table, and the sanitized error message for failed/cancelled runs.
// No remote assets, no endpoints, no server identity.
func (s *SMTP) renderHTML(subject string, payload Payload) string {
	var body strings.Builder
	fmt.Fprintf(&body, `<!DOCTYPE html>
<html>
<body style="margin:0;padding:0;background:#f6f8fa;font-family:sans-serif;">
<div style="max-width:600px;margin:24px auto;background:#ffffff;border-top:4px solid #%06X;border-radius:4px;">
<h2 style="margin:0;padding:16px 24px;font-size:18px;">%s</h2>
<table style="width:100%%;border-collapse:collapse;font-size:14px;">
`, statusColor(payload.Status), html.EscapeString(subject))
	rows := []struct{ name, value string }{
		{"Site", payload.Site},
		{"Status", payload.Status},
		{"Started", payload.StartedAt},
		{"Finished", payload.FinishedAt},
		{"Duration", fmt.Sprintf("%ds", payload.DurationSeconds)},
		{"Artifacts", strconv.Itoa(payload.ArtifactCount)},
		{"Size", formatBytes(payload.SizeBytes)},
	}
	for _, row := range rows {
		fmt.Fprintf(&body, `<tr><td style="padding:8px 24px;color:#586069;">%s</td><td style="padding:8px 24px;">%s</td></tr>
`, row.name, html.EscapeString(row.value))
	}
	if payload.ErrorMessage != "" {
		fmt.Fprintf(&body, `<tr><td style="padding:8px 24px;color:#586069;">Error</td><td style="padding:8px 24px;"><strong>%s</strong>: %s</td></tr>
`, html.EscapeString(payload.ErrorCategory), html.EscapeString(payload.ErrorMessage))
	}
	body.WriteString(`</table>
</div>
</body>
</html>
`)
	return body.String()
}

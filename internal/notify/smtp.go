package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
)

// SMTP sends one branded HTML email per notification. Transport rules:
// port 465 is implicit TLS; any other port uses STARTTLS when the server
// offers it. PLAIN credentials are used only when username/password values are
// present and the session is encrypted (implicit TLS or STARTTLS); a server
// without STARTTLS fails the channel instead of sending credentials in
// cleartext. Unauthenticated SMTP is allowed.
type SMTP struct {
	name     string
	host     string
	port     int
	username string
	password string
	from     string
	to       []string
	// roots overrides the TLS root pool; nil means system roots. Tests
	// inject a pool containing the fake server's self-signed certificate.
	roots *x509.CertPool
	// implicitTLS is true for port 465 (and in tests, for an ephemeral
	// port simulating it).
	implicitTLS bool
}

func NewSMTP(name string, channel config.Channel, roots *x509.CertPool) *SMTP {
	return &SMTP{
		name:        name,
		host:        channel.Host,
		port:        channel.Port,
		username:    channel.Username,
		password:    channel.Password,
		from:        channel.From,
		to:          channel.To,
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

	subject := headline(payload)
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

// credentials returns the SMTP credentials loaded from the protected root
// configuration file.
func (s *SMTP) credentials() (username, password string, err error) {
	if s.username == "" {
		return "", "", nil
	}
	return s.username, s.password, nil
}

const logoContentID = "bqckup-logo"

func base64MIME(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var builder strings.Builder
	for len(encoded) > 76 {
		builder.WriteString(encoded[:76])
		builder.WriteString("\r\n")
		encoded = encoded[76:]
	}
	if len(encoded) > 0 {
		builder.WriteString(encoded)
		builder.WriteString("\r\n")
	}
	return builder.String()
}

func (s *SMTP) renderMessage(subject string, payload Payload) string {
	boundary := fmt.Sprintf("bqckup_%d", time.Now().UnixNano())
	var builder strings.Builder
	fmt.Fprintf(&builder, "From: %s\r\n", s.from)
	fmt.Fprintf(&builder, "To: %s\r\n", strings.Join(s.to, ", "))
	fmt.Fprintf(&builder, "Subject: %s\r\n", subject)
	builder.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&builder, "Content-Type: multipart/related; boundary=\"%s\"\r\n", boundary)
	builder.WriteString("\r\n")

	// HTML part
	fmt.Fprintf(&builder, "--%s\r\n", boundary)
	builder.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	builder.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	builder.WriteString("\r\n")
	builder.WriteString(s.renderHTML(subject, payload, "cid:"+logoContentID))
	builder.WriteString("\r\n\r\n")

	// Inline logo part
	if len(logoPNG) > 0 {
		fmt.Fprintf(&builder, "--%s\r\n", boundary)
		builder.WriteString("Content-Type: image/png\r\n")
		builder.WriteString("Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&builder, "Content-ID: <%s>\r\n", logoContentID)
		builder.WriteString("Content-Disposition: inline; filename=\"logo-bqckup.png\"\r\n")
		builder.WriteString("\r\n")
		builder.WriteString(base64MIME(logoPNG))
		builder.WriteString("\r\n")
	}

	fmt.Fprintf(&builder, "--%s--\r\n", boundary)
	return builder.String()
}

// renderHTML builds the branded email body: a dark navy header bar with the
// Bqckup logo, a status color accent line, the headline title, description
// paragraph, vertical table rows with row dividers, and on failure a What went
// wrong row, Try this suggestion, and closing monitoring footer. No remote
// assets and no endpoints.
func (s *SMTP) renderHTML(subject string, payload Payload, logoSrc ...string) string {
	src := "cid:" + logoContentID
	if len(logoSrc) > 0 && logoSrc[0] != "" {
		src = logoSrc[0]
	}

	lastSuccess := "No successful backup yet"
	if payload.LastSuccessfulAt != "" {
		if t, err := time.Parse(time.RFC3339, payload.LastSuccessfulAt); err == nil {
			lastSuccess = lastSuccessfulLine(t)
		} else {
			lastSuccess = payload.LastSuccessfulAt
		}
	}

	rows := []struct{ name, value string }{
		{"Server", serverLine(payload.Hostname, payload.ServerIP)},
		{"Last Successful Backup", lastSuccess},
		{"Duration", durationHuman(payload.DurationSeconds)},
	}

	var label, message string
	if payload.Status == string(backup.StatusFailed) || payload.Status == string(backup.StatusNoChange) {
		label, message = failureBlock(payload)
		rows = append(rows,
			struct{ name, value string }{"Consecutive Failures", fmt.Sprintf("%d", payload.FailureStreak)},
			struct{ name, value string }{"Problem faced", label},
		)
		if message != "" {
			rows = append(rows, struct{ name, value string }{"What went wrong", message})
		}
	}

	var body strings.Builder
	fmt.Fprintf(&body, `<!DOCTYPE html>
<html>
<body style="margin:0;padding:0;background:#f6f8fa;font-family:sans-serif;">
<div style="max-width:600px;margin:24px auto;background:#ffffff;border-radius:6px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
<div style="background:#0c193e;padding:16px 24px;">
<table style="border-collapse:collapse;">
<tr>
<td style="padding:0;vertical-align:middle;">
<div style="width:34px;height:34px;background:#ffffff;border-radius:8px;padding:3px;box-sizing:border-box;">
<img src="%s" width="28" height="28" style="display:block;border-radius:5px;" alt="Bqckup" />
</div>
</td>
<td style="padding:0 0 0 12px;vertical-align:middle;font-size:18px;font-weight:700;color:#ffffff;font-family:sans-serif;">
Bqckup
</td>
</tr>
</table>
</div>
<div style="height:3px;background:#%06X;"></div>
<h2 style="margin:0;padding:24px 24px 12px;font-size:22px;font-weight:700;color:#1f2328;line-height:1.3;">%s</h2>
<p style="padding:0 24px 16px;margin:0;font-size:14px;color:#586069;line-height:1.5;">%s</p>
<table style="width:100%%;border-collapse:collapse;font-size:14px;">
`, src, statusColor(payload.Status), html.EscapeString(subject), html.EscapeString(description(payload)))

	for _, row := range rows {
		val := html.EscapeString(row.value)
		val = strings.ReplaceAll(val, "\n", "<br>")
		fmt.Fprintf(&body, `<tr><td style="padding:10px 24px;font-weight:600;color:#24292e;border-top:1px solid #e1e4e8;white-space:nowrap;width:38%%;">%s</td><td style="padding:10px 24px;color:#24292e;border-top:1px solid #e1e4e8;">%s</td></tr>
`, html.EscapeString(row.name), val)
	}
	body.WriteString(`</table>
`)

	fmt.Fprintf(&body, `<p style="padding:16px 24px 24px;margin:0;border-top:1px solid #e1e4e8;font-size:12px;color:#586069;">%s</p>
</div>
</body>
</html>
`, html.EscapeString(monitoringFooter(time.Now())))

	return body.String()
}

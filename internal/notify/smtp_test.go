package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSMTPServer is a minimal in-memory SMTP server for tests: banner, EHLO
// capabilities, optional STARTTLS (self-signed) and implicit TLS, PLAIN AUTH,
// and MAIL/RCPT/DATA/QUIT. It records the envelope and message body.
type fakeSMTPServer struct {
	addr        string
	cert        tls.Certificate
	roots       *x509.CertPool
	offerTLS    bool
	implicitTLS bool

	mu         sync.Mutex
	recipients []string
	message    string
	authSeen   bool
}

func newFakeSMTPServer(t *testing.T, offerTLS, implicitTLS bool) *fakeSMTPServer {
	t.Helper()
	server := &fakeSMTPServer{offerTLS: offerTLS, implicitTLS: implicitTLS}
	if offerTLS || implicitTLS {
		server.cert, server.roots = selfSignedCert(t)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	server.addr = listener.Addr().String()
	go server.serve(listener)
	return server
}

func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func (s *fakeSMTPServer) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTPServer) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if s.implicitTLS {
		// Implicit TLS: the session starts encrypted; the banner is
		// sent after the handshake.
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}})
		if err := tlsConn.Handshake(); err != nil {
			return
		}
		conn = tlsConn
	}
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	reply := func(format string, args ...any) bool {
		_, err := fmt.Fprintf(writer, format+"\r\n", args...)
		if err != nil {
			return false
		}
		return writer.Flush() == nil
	}

	if !reply("220 fake ESMTP bqckup-test") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		parts := strings.SplitN(strings.TrimRight(line, "\r\n"), " ", 2)
		command := strings.ToUpper(parts[0])
		arg := ""
		if len(parts) == 2 {
			arg = parts[1]
		}
		switch command {
		case "EHLO", "HELO":
			if s.implicitTLS || !s.offerTLS {
				if !reply("250-fake ESMTP\r\n250 8BITMIME") {
					return
				}
				continue
			}
			// The final line must carry no dash: the client stops
			// reading the reply there.
			if !reply("250-fake ESMTP\r\n250-STARTTLS\r\n250-AUTH PLAIN LOGIN\r\n250 8BITMIME") {
				return
			}
		case "STARTTLS":
			if !reply("220 Ready to start TLS") {
				return
			}
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			reader = bufio.NewReader(tlsConn)
			writer = bufio.NewWriter(tlsConn)
		case "AUTH":
			s.mu.Lock()
			s.authSeen = true
			s.mu.Unlock()
			if strings.HasPrefix(arg, "PLAIN") {
				// AUTH PLAIN <base64> carries the credentials; only the
				// fact of authentication matters for assertions.
				if !reply("235 2.7.0 Authentication successful") {
					return
				}
				continue
			}
			if !reply("334 ") {
				return
			}
			// Unused: the client sends credentials next; tests use PLAIN.
		case "MAIL":
			if !reply("250 2.1.0 OK") {
				return
			}
		case "RCPT":
			recipient := arg
			if open := strings.LastIndex(recipient, "<"); open >= 0 {
				recipient = recipient[open+1:]
			}
			recipient = strings.TrimSuffix(recipient, ">")
			s.mu.Lock()
			s.recipients = append(s.recipients, recipient)
			s.mu.Unlock()
			if !reply("250 2.1.5 OK") {
				return
			}
		case "DATA":
			if !reply("354 End data with <CR><LF>.<CR><LF>") {
				return
			}
			var body strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
				body.WriteString(dataLine)
			}
			s.mu.Lock()
			s.message = body.String()
			s.mu.Unlock()
			if !reply("250 2.0.0 OK") {
				return
			}
		case "QUIT":
			_ = reply("221 2.0.0 Bye")
			return
		default:
			if !reply("250 OK") {
				return
			}
		}
	}
}

func (s *fakeSMTPServer) snapshot() (recipients []string, message string, authSeen bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.recipients...), s.message, s.authSeen
}

func smtpChannel(t *testing.T, channel config.Channel, lookupEnv func(string) (string, bool), roots *x509.CertPool) *SMTP {
	t.Helper()
	return NewSMTP("email", channel, lookupEnv, roots)
}

func smtpPayload() Payload {
	input := notifyInput(config.EventBackupSucceeded)
	input.Status = backup.StatusSuccess
	input.StartedAt = time.Date(2026, 8, 23, 1, 46, 56, 0, time.UTC)
	input.FinishedAt = time.Date(2026, 8, 23, 1, 48, 38, 0, time.UTC)
	input.Artifacts = []history.Artifact{{SourceKind: "files", SourceName: "files", Size: 2048}}
	return NewPayload(input)
}

func TestSMTPDeliversPlainMessageWithoutAuth(t *testing.T) {
	server := newFakeSMTPServer(t, false, false)
	channel := smtpChannel(t, config.Channel{
		Type: "smtp", Host: "127.0.0.1", Port: portOf(t, server.addr),
		From: "bqckup@example.com", To: []string{"ops@example.com"},
	}, func(string) (string, bool) { return "", false }, nil)

	require.NoError(t, channel.Send(context.Background(), smtpPayload()))

	recipients, message, authSeen := server.snapshot()
	assert.Equal(t, []string{"ops@example.com"}, recipients)
	assert.False(t, authSeen)
	assert.Contains(t, message, "Subject: [bqckup] backup_succeeded: example.org")
	assert.Contains(t, message, "From: bqckup@example.com")
	assert.Contains(t, message, "example.org")
	assert.Contains(t, message, "Artifacts")
}

func TestSMTPUsesSTARTTLSAndAuthWhenConfigured(t *testing.T) {
	server := newFakeSMTPServer(t, true, false)
	channel := smtpChannel(t, config.Channel{
		Type: "smtp", Host: "127.0.0.1", Port: portOf(t, server.addr),
		UsernameEnv: "BQCKUP_SMTP_USERNAME", PasswordEnv: "BQCKUP_SMTP_PASSWORD",
		From: "bqckup@example.com", To: []string{"ops@example.com"},
	}, func(key string) (string, bool) {
		switch key {
		case "BQCKUP_SMTP_USERNAME":
			return "backup-sender", true
		case "BQCKUP_SMTP_PASSWORD":
			return "hunter2-secret", true
		}
		return "", false
	}, server.roots)

	require.NoError(t, channel.Send(context.Background(), smtpPayload()))

	recipients, message, authSeen := server.snapshot()
	assert.Equal(t, []string{"ops@example.com"}, recipients)
	assert.True(t, authSeen, "AUTH PLAIN must run over the STARTTLS session")
	assert.Contains(t, message, "Subject: [bqckup] backup_succeeded: example.org")
	assert.NotContains(t, message, "hunter2-secret")
	assert.NotContains(t, message, "backup-sender")
}

func TestSMTPRefusesAuthWithoutSTARTTLS(t *testing.T) {
	server := newFakeSMTPServer(t, false, false)
	channel := smtpChannel(t, config.Channel{
		Type: "smtp", Host: "127.0.0.1", Port: portOf(t, server.addr),
		UsernameEnv: "BQCKUP_SMTP_USERNAME", PasswordEnv: "BQCKUP_SMTP_PASSWORD",
		From: "bqckup@example.com", To: []string{"ops@example.com"},
	}, func(key string) (string, bool) {
		if key == "BQCKUP_SMTP_USERNAME" || key == "BQCKUP_SMTP_PASSWORD" {
			return "secret", true
		}
		return "", false
	}, nil)

	err := channel.Send(context.Background(), smtpPayload())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "STARTTLS")
	_, _, authSeen := server.snapshot()
	assert.False(t, authSeen, "credentials must never be sent over a cleartext connection")
}

func TestSMTPImplicitTLSOnPort465(t *testing.T) {
	server := newFakeSMTPServer(t, false, true)
	channel := smtpChannel(t, config.Channel{
		Type: "smtp", Host: "127.0.0.1", Port: portOf(t, server.addr),
		UsernameEnv: "BQCKUP_SMTP_USERNAME", PasswordEnv: "BQCKUP_SMTP_PASSWORD",
		From: "bqckup@example.com", To: []string{"ops@example.com"},
	}, func(key string) (string, bool) {
		if key == "BQCKUP_SMTP_USERNAME" || key == "BQCKUP_SMTP_PASSWORD" {
			return "secret", true
		}
		return "", false
	}, server.roots)
	// Simulate a 465 listener: implicit TLS on the ephemeral port.
	channel.implicitTLS = true

	require.NoError(t, channel.Send(context.Background(), smtpPayload()))

	recipients, message, authSeen := server.snapshot()
	assert.Equal(t, []string{"ops@example.com"}, recipients)
	assert.True(t, authSeen)
	assert.Contains(t, message, "backup_succeeded")
}

func TestSMTPImplicitTLSFlagSetForPort465(t *testing.T) {
	channel := NewSMTP("email", config.Channel{
		Type: "smtp", Host: "smtp.example.com", Port: 465,
	}, nil, nil)
	assert.True(t, channel.implicitTLS)
}

func TestSMTPMissingEnvIsAnError(t *testing.T) {
	server := newFakeSMTPServer(t, true, false)
	channel := smtpChannel(t, config.Channel{
		Type: "smtp", Host: "127.0.0.1", Port: portOf(t, server.addr),
		UsernameEnv: "BQCKUP_SMTP_USERNAME", PasswordEnv: "BQCKUP_SMTP_PASSWORD",
		From: "bqckup@example.com", To: []string{"ops@example.com"},
	}, func(string) (string, bool) { return "", false }, server.roots)

	err := channel.Send(context.Background(), smtpPayload())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BQCKUP_SMTP_USERNAME")
}

func portOf(t *testing.T, address string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	require.NoError(t, err)
	var value int
	_, err = fmt.Sscanf(port, "%d", &value)
	require.NoError(t, err)
	return value
}

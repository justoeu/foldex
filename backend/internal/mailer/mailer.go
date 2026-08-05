// Package mailer delivers the transactional e-mail the auth stack depends on:
// invites now, password resets and OTP codes from PR3 on.
//
// Two drivers ship. `smtp` talks to a real server (or Mailpit in dev); `log`
// writes the message to the structured log instead of sending it. The log
// driver is the DEFAULT, and that is deliberate: foldex is self-hosted, so an
// operator who never configures SMTP must still be able to complete an invite
// flow by reading the link out of `docker compose logs`. Failing closed there
// would strand the very first admin trying to add a second user.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Message is one outbound e-mail. HTML is optional; Text is not, so every
// message stays readable in a client that refuses HTML.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer sends transactional mail.
//
// Send returning an error is NOT a reason to fail the request that triggered
// it. An invite whose e-mail bounced still exists and can be re-sent or
// copied from the admin screen; rolling the invite back because the SMTP host
// was briefly down would be strictly worse. Callers log and continue.
type Mailer interface {
	Send(ctx context.Context, m Message) error
	// Driver names the active transport, surfaced through /api/auth/me's
	// feature flags so the UI can tell the user "check the logs" instead of
	// "check your inbox" when no real SMTP is configured.
	Driver() string
}

// Config is the SMTP wiring, read from the environment by internal/config.
type Config struct {
	Driver   string // "log" (default) or "smtp"
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	// STARTTLS upgrades a plaintext connection. Ignored when TLS is set.
	STARTTLS bool
	// TLS dials straight into TLS (implicit TLS, usually port 465).
	TLS bool
	// InsecureSkipVerify disables certificate verification. Exists ONLY for
	// self-signed dev servers; validateSecureDefaults refuses it together with
	// a non-loopback host.
	InsecureSkipVerify bool
	Timeout            time.Duration
}

// New returns the mailer for cfg. An unknown driver is an error rather than a
// silent fallback: "I set MAIL_DRIVER=smpt and mail silently went to the log"
// is a much worse failure than refusing to boot.
func New(cfg Config, logger *slog.Logger) (Mailer, error) {
	switch cfg.Driver {
	case "", "log":
		return &logMailer{logger: logger}, nil
	case "smtp":
		if cfg.Host == "" {
			return nil, errors.New("mailer: MAIL_DRIVER=smtp requires MAIL_HOST")
		}
		if cfg.From == "" {
			return nil, errors.New("mailer: MAIL_DRIVER=smtp requires MAIL_FROM")
		}
		if cfg.Timeout <= 0 {
			cfg.Timeout = 10 * time.Second
		}
		return &smtpMailer{cfg: cfg, logger: logger}, nil
	default:
		return nil, fmt.Errorf("mailer: unknown MAIL_DRIVER %q (want \"log\" or \"smtp\")", cfg.Driver)
	}
}

// logMailer writes the message to the structured log.
//
// It logs the full body ON PURPOSE — including the invite/reset link, which is
// a credential. That is the whole point of the driver: on a self-hosted
// instance with no SMTP, the log IS the mailbox, and the operator reading it is
// the same person who owns the instance. Anyone who can read foldex's stdout
// can already read its database.
type logMailer struct{ logger *slog.Logger }

func (m *logMailer) Driver() string { return "log" }

func (m *logMailer) Send(_ context.Context, msg Message) error {
	m.logger.Info("mail (log driver — no SMTP configured)",
		"to", msg.To,
		"subject", msg.Subject,
		"body", msg.Text,
	)
	return nil
}

type smtpMailer struct {
	cfg    Config
	logger *slog.Logger
}

func (m *smtpMailer) Driver() string { return "smtp" }

func (m *smtpMailer) Send(ctx context.Context, msg Message) error {
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprint(m.cfg.Port))
	body := m.render(msg)

	dialer := &net.Dialer{Timeout: m.cfg.Timeout}
	var conn net.Conn
	var err error
	if m.cfg.TLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, m.tlsConfig())
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mailer: dial %s: %w", addr, err)
	}
	// The deadline covers the whole SMTP conversation. Without it a server that
	// accepts the connection and then stops responding pins the goroutine (and
	// the caller's request) until the process dies.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(m.cfg.Timeout))
	}

	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer: smtp handshake: %w", err)
	}
	defer func() { _ = c.Close() }()

	if m.cfg.STARTTLS && !m.cfg.TLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("mailer: MAIL_STARTTLS=1 but the server does not advertise STARTTLS")
		}
		if err := c.StartTLS(m.tlsConfig()); err != nil {
			return fmt.Errorf("mailer: starttls: %w", err)
		}
	}

	if m.cfg.Username != "" {
		// PlainAuth refuses to send credentials over an unencrypted link unless
		// the host is localhost. That check is net/smtp's, and we keep it:
		// silently leaking SMTP credentials in cleartext is not a trade a
		// convenience flag should be able to make.
		auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("mailer: auth: %w", err)
		}
	}
	if err := c.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("mailer: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(msg.To); err != nil {
		return fmt.Errorf("mailer: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mailer: DATA: %w", err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		_ = w.Close()
		return fmt.Errorf("mailer: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: close body: %w", err)
	}
	return c.Quit()
}

func (m *smtpMailer) tlsConfig() *tls.Config {
	return &tls.Config{
		ServerName: m.cfg.Host,
		MinVersion: tls.VersionTLS12,
		//nolint:gosec // G402: opt-in, refused alongside a non-loopback host by config.validateSecureDefaults.
		InsecureSkipVerify: m.cfg.InsecureSkipVerify,
	}
}

// render builds the RFC 5322 message.
//
// Header values are sanitized against CR/LF before they are written. Subject
// and To ultimately derive from user-controlled data (an admin types the invite
// address; a display name reaches the subject line), and a bare newline there
// is header injection: the attacker closes the header block early and appends
// their own Bcc, or a whole second message.
func (m *smtpMailer) render(msg Message) string {
	from := sanitizeHeader(m.cfg.From)
	if m.cfg.FromName != "" {
		// Sanitize BEFORE Q-encoding. Encoding alone would already be safe —
		// mime.QEncoding turns CR/LF into =0D=0A inside an encoded-word, which
		// no parser treats as a line break — but stripping first means the raw
		// bytes never reach the header at all, so the safety does not depend on
		// the encoder's behaviour staying the same.
		from = fmt.Sprintf("%s <%s>",
			mime.QEncoding.Encode("utf-8", sanitizeHeader(m.cfg.FromName)), from)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", sanitizeHeader(msg.To))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", sanitizeHeader(msg.Subject)))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	// Auto-Submitted marks these as machine-generated so a recipient's
	// autoresponder does not bounce a vacation reply back at the instance.
	b.WriteString("Auto-Submitted: auto-generated\r\n")

	if msg.HTML == "" {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(dotStuff(msg.Text))
		return b.String()
	}

	const boundary = "foldex-mixed-boundary"
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, dotStuff(msg.Text))
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=utf-8\r\n\r\n%s\r\n", boundary, dotStuff(msg.HTML))
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.String()
}

// sanitizeHeader strips CR and LF so a value cannot terminate its own header
// and inject another.
func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(v)
}

// dotStuff escapes a leading "." on any line, per RFC 5321 §4.5.2. A body line
// consisting of a single dot ends the DATA command; without stuffing, content
// that happens to start a line with "." truncates the message.
func dotStuff(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, ".") {
			lines[i] = "." + ln
		}
	}
	return strings.Join(lines, "\r\n")
}

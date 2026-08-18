package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
}

func TestNewDefaultsToLogDriver(t *testing.T) {
	t.Parallel()
	m, err := New(Config{}, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, "log", m.Driver(),
		"a self-hosted instance with no SMTP must still be able to complete an invite flow")
}

func TestNewRejectsUnknownDriver(t *testing.T) {
	t.Parallel()
	_, err := New(Config{Driver: "smpt"}, discardLogger())
	// Silently falling back to the log driver on a typo would send invite links
	// to the log while the operator waits for an inbox that never fills.
	assert.ErrorContains(t, err, "unknown MAIL_DRIVER")
}

func TestNewSMTPRequiresHostAndFrom(t *testing.T) {
	t.Parallel()
	_, err := New(Config{Driver: "smtp"}, discardLogger())
	assert.ErrorContains(t, err, "MAIL_HOST")

	_, err = New(Config{Driver: "smtp", Host: "mail.example"}, discardLogger())
	assert.ErrorContains(t, err, "MAIL_FROM")
}

func TestLogMailerWritesTheBody(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	m, err := New(Config{Driver: "log"}, slog.New(slog.NewJSONHandler(&buf, nil)))
	require.NoError(t, err)

	require.NoError(t, m.Send(context.Background(), Message{
		To: "a@b.com", Subject: "hi", Text: "https://foldex.local/?invite=SECRET",
	}))

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, "a@b.com", rec["to"])
	// The link IS logged, deliberately — with no SMTP the log is the mailbox.
	assert.Contains(t, rec["body"], "invite=SECRET")
}

// headerNames returns the field name of every header LINE in the rendered
// message.
//
// Asserting on header LINES, not on substrings, is the distinction that
// matters. Once the CR/LF is stripped, `victim@x\r\nBcc: evil@y` collapses to
// the single value `victim@xBcc: evil@y` — the literal text "Bcc:" is still in
// the message, and is completely inert, because it is part of the To value
// rather than a field of its own. A NotContains("Bcc:") assertion would fail on
// safe output and pass on nothing useful.
func headerNames(t *testing.T, body string) []string {
	t.Helper()
	headers, _, ok := strings.Cut(body, "\r\n\r\n")
	require.True(t, ok, "message must have a header/body separator")
	var names []string
	for _, line := range strings.Split(headers, "\r\n") {
		name, _, ok := strings.Cut(line, ":")
		require.True(t, ok, "header line %q has no colon", line)
		names = append(names, name)
	}
	return names
}

// Header injection. The invite address is typed by an admin and the subject can
// carry a display name; a bare CR or LF there would close the header block
// early and let the rest of the value become new headers — a Bcc, or a whole
// second message.
func TestRenderStripsHeaderInjection(t *testing.T) {
	t.Parallel()
	m := &smtpMailer{cfg: Config{From: "foldex@localhost"}}

	body := m.render(Message{
		To:      "victim@example.com\r\nBcc: attacker@evil.com",
		Subject: "Invite\r\nX-Injected: yes",
		Text:    "hello",
	})

	names := headerNames(t, body)
	assert.NotContains(t, names, "Bcc", "CRLF in To must not spawn a Bcc header")
	assert.NotContains(t, names, "X-Injected", "CRLF in Subject must not spawn a header")
	assert.Equal(t, []string{"From", "To", "Subject", "Date", "MIME-Version", "Auto-Submitted", "Content-Type"},
		names, "exactly the headers render() writes, and no others")
}

func TestRenderSanitizesTheFromName(t *testing.T) {
	t.Parallel()
	m := &smtpMailer{cfg: Config{From: "foldex@localhost", FromName: "Foldex\r\nBcc: x@evil"}}
	body := m.render(Message{To: "a@b.com", Subject: "s", Text: "t"})

	assert.NotContains(t, headerNames(t, body), "Bcc")
	// The CR/LF is stripped before Q-encoding, so it does not even survive as
	// an escaped =0D=0A inside the encoded-word.
	assert.NotContains(t, body, "=0D", "raw CR must be stripped, not merely encoded")
}

// RFC 5321 §4.5.2: a line consisting of a single "." terminates DATA. Without
// stuffing, a body whose line starts with "." truncates the message there.
func TestRenderDotStuffsBodyLines(t *testing.T) {
	t.Parallel()
	m := &smtpMailer{cfg: Config{From: "f@l"}}
	body := m.render(Message{To: "a@b.com", Subject: "s", Text: "line one\n.\nline two"})

	_, payload, ok := strings.Cut(body, "\r\n\r\n")
	require.True(t, ok)
	assert.Contains(t, payload, "\r\n..\r\n", "a lone dot line must be escaped to ..")
	assert.Contains(t, payload, "line two", "content after the dot must survive")
}

func TestRenderMultipartWhenHTMLPresent(t *testing.T) {
	t.Parallel()
	m := &smtpMailer{cfg: Config{From: "f@l"}}
	body := m.render(Message{To: "a@b.com", Subject: "s", Text: "plain", HTML: "<p>rich</p>"})

	assert.Contains(t, body, "multipart/alternative")
	assert.Contains(t, body, "text/plain")
	assert.Contains(t, body, "text/html")
	assert.Contains(t, body, "plain")
	assert.Contains(t, body, "<p>rich</p>")
}

func TestRenderPlainWhenNoHTML(t *testing.T) {
	t.Parallel()
	m := &smtpMailer{cfg: Config{From: "f@l"}}
	body := m.render(Message{To: "a@b.com", Subject: "s", Text: "plain"})

	assert.Contains(t, body, "Content-Type: text/plain")
	assert.NotContains(t, body, "multipart")
}

// mustRender resolves an envelope the way the relay does. The constructors no
// longer produce a body — they name a template — so every assertion about copy
// has to go through the same path a real send takes.
func mustRender(t *testing.T, env Envelope, locale string) Message {
	t.Helper()
	m, err := Render(env, locale)
	if err != nil {
		t.Fatalf("render %s/%s: %v", env.Template, locale, err)
	}
	return m
}

func TestInviteMessageCarriesTheLink(t *testing.T) {
	t.Parallel()
	msg := mustRender(t, InviteMessage("new@example.com", "Ana", "https://foldex.local/#invite=TOK", 168), "en")

	assert.Equal(t, "new@example.com", msg.To)
	assert.Contains(t, msg.Text, "https://foldex.local/#invite=TOK")
	assert.Contains(t, msg.HTML, "https://foldex.local/#invite=TOK")
	assert.Contains(t, msg.Text, "Ana")
	assert.Contains(t, msg.Text, "168 hours")
}

func TestInviteMessageEscapesHTML(t *testing.T) {
	t.Parallel()
	msg := mustRender(t, InviteMessage("a@b.com", `Ana"><script>alert(1)</script>`, "https://x/#invite=T", 1), "en")
	assert.NotContains(t, msg.HTML, "<script>", "an inviter name must not break out of its element")
}

func TestAdminPasswordRecoveryMessageCarriesOnlyTheTargetsChoiceLink(t *testing.T) {
	t.Parallel()
	msg := mustRender(t, AdminPasswordRecoveryMessage("target@example.com", "https://foldex.local/#reset=TOK", 30), "en")

	assert.Equal(t, "target@example.com", msg.To)
	assert.Contains(t, msg.Text, "https://foldex.local/#reset=TOK")
	assert.Contains(t, msg.HTML, "https://foldex.local/#reset=TOK")
	assert.Contains(t, msg.Text, "choose your own new password")
	assert.NotContains(t, strings.ToLower(msg.Text), "temporary password")
}

// The reuse warning must carry no link: an unexpected "your session was
// terminated, click here" is exactly the shape of a phishing mail, and training
// users to click it is worse than the warning is worth.
//
// Asserting on the ABSENCE of an anchor rather than on an empty HTML arm: every
// message renders through the shared layout now, so "no HTML" stopped being how
// a linkless message looks.
func TestSessionRevokedMessageHasNoLink(t *testing.T) {
	t.Parallel()
	for _, locale := range SupportedLocales() {
		msg := mustRender(t, SessionRevokedMessage("a@b.com"), locale)
		assert.Equal(t, "a@b.com", msg.To)
		assert.NotContains(t, msg.Text, "http", locale)
		assert.NotContains(t, msg.HTML, "<a ", locale)
	}
}

func TestPasswordResetUnavailableCarriesNoLink(t *testing.T) {
	t.Parallel()
	for _, locale := range SupportedLocales() {
		msg := mustRender(t, PasswordResetUnavailableMessage("a@b.com"), locale)
		assert.NotContains(t, msg.Text, "http", locale)
		assert.NotContains(t, msg.HTML, "<a ", locale)
	}
}

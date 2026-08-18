package mailworker

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
	"foldex/internal/mailoutbox"
)

// fakeMailer records what it was handed and fails on demand.
//
// Guarded because the integration tests read it from the test goroutine while
// the worker's consume loop writes to it — unsynchronized, that is a race the
// detector fails on rather than a flake anyone would diagnose.
type fakeMailer struct {
	mu   sync.Mutex
	err  error
	sent []mailer.Message
}

func (f *fakeMailer) Driver() string { return "smtp" }
func (f *fakeMailer) Send(_ context.Context, m mailer.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return f.err
}

func (f *fakeMailer) delivered() []mailer.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mailer.Message(nil), f.sent...)
}

func testOutbox(t *testing.T) (*mailoutbox.Outbox, []byte) {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	o, err := mailoutbox.NewFromMasterKey(key)
	require.NoError(t, err)
	return o, key
}

// seal produces a WireMessage the way the relay would, through the real cipher.
func seal(t *testing.T, o *mailoutbox.Outbox, template, locale string, params map[string]string) mailoutbox.WireMessage {
	t.Helper()
	ct, nonce, err := o.Seal(params)
	require.NoError(t, err)
	return mailoutbox.WireMessage{
		OutboxID: 1, Template: template, Recipient: "grace@x.test", Locale: locale,
		Ciphertext: ct, Nonce: nonce,
	}
}

func newTestWorker(t *testing.T, o *mailoutbox.Outbox, m mailer.Mailer) *Worker {
	t.Helper()
	w := New(o, m, mailoutbox.AMQPConfig{URL: "amqp://127.0.0.1:1/"}, Options{}, discard())
	// send() reads w.ctx for its timeout; Start would also spin up a consume
	// loop against a broker that is not there.
	w.ctx, w.cancel = context.WithCancel(context.Background())
	t.Cleanup(w.cancel)
	return w
}

func TestSend_DeliversAndReportsNoFailure(t *testing.T) {
	o, _ := testOutbox(t)
	m := &fakeMailer{}
	w := newTestWorker(t, o, m)

	reason, fatal, err := w.send(seal(t, o, mailer.TemplateLoginCode, "pt",
		loginCodeParams()))

	require.NoError(t, err)
	require.False(t, fatal)
	require.Empty(t, reason)
	require.Len(t, m.sent, 1)
	require.Equal(t, "grace@x.test", m.sent[0].To)
	// The text arm is mandatory on every message — render only emits
	// multipart/alternative when both exist.
	require.NotEmpty(t, m.sent[0].Text)
}

// The locale on the wire is what decides the language, not the process default.
// Getting this wrong would send every message in English while the profile said
// otherwise, and nothing would error.
func TestSend_RendersInTheMessagesOwnLocale(t *testing.T) {
	o, _ := testOutbox(t)
	pt, en := &fakeMailer{}, &fakeMailer{}

	_, _, err := newTestWorker(t, o, pt).send(
		seal(t, o, mailer.TemplateLoginCode, "pt", loginCodeParams()))
	require.NoError(t, err)
	_, _, err = newTestWorker(t, o, en).send(
		seal(t, o, mailer.TemplateLoginCode, "en", loginCodeParams()))
	require.NoError(t, err)

	require.NotEqual(t, pt.sent[0].Subject, en.sent[0].Subject,
		"a pt message and an en message must not render identically")
}

// A payload that will not decrypt is FATAL. Retrying it walks the whole ladder
// and then dead-letters it half an hour later, delaying the only thing that
// actually helps: an operator noticing the key changed.
func TestSend_UndecryptablePayloadIsPermanent(t *testing.T) {
	o, _ := testOutbox(t)
	other, _ := testOutbox(t) // a different master key
	m := &fakeMailer{}
	w := newTestWorker(t, o, m)

	msg := seal(t, other, mailer.TemplateLoginCode, "en", loginCodeParams())
	reason, fatal, err := w.send(msg)

	require.Error(t, err)
	require.True(t, fatal)
	require.Equal(t, "undecryptable_payload", reason)
	require.Empty(t, m.sent, "nothing may be sent when the payload could not be opened")
}

// Same reasoning: a template this binary does not ship will not appear on the
// next attempt.
func TestSend_UnknownTemplateIsPermanent(t *testing.T) {
	o, _ := testOutbox(t)
	m := &fakeMailer{}
	w := newTestWorker(t, o, m)

	reason, fatal, err := w.send(seal(t, o, "no_such_template", "en", nil))

	require.Error(t, err)
	require.True(t, fatal)
	require.Equal(t, "unknown_template", reason)
	require.Empty(t, m.sent)
}

// A render failure is fatal too — templates run with missingkey=error, so a
// param a constructor forgot fails here rather than printing "<no value>" into
// a password-reset e-mail.
func TestSend_MissingTemplateParamIsPermanentAndSendsNothing(t *testing.T) {
	o, _ := testOutbox(t)
	m := &fakeMailer{}
	w := newTestWorker(t, o, m)

	// password_reset needs an action URL; withholding it must not produce a
	// message with a blank link in it.
	reason, fatal, err := w.send(seal(t, o, mailer.TemplatePasswordReset, "en", map[string]string{}))

	require.Error(t, err)
	require.True(t, fatal)
	require.Equal(t, "render_failed", reason)
	require.Empty(t, m.sent)
}

// A transport failure is the ONLY non-fatal class: it is the one that gets
// better on its own, and it is what the retry ladder exists for.
func TestSend_TransportFailureIsRetryableAndDoesNotQuoteTheTransport(t *testing.T) {
	o, _ := testOutbox(t)
	m := &fakeMailer{err: errors.New("550 5.1.1 <grace@x.test>: recipient rejected")}
	w := newTestWorker(t, o, m)

	reason, fatal, err := w.send(seal(t, o, mailer.TemplateLoginCode, "en",
		loginCodeParams()))

	require.Error(t, err)
	require.False(t, fatal, "a provider refusal must ride the ladder, not settle at once")
	require.Equal(t, "send_failed", reason)
	require.NotContains(t, reason, "grace@x.test")
}

// Shutdown cancels the send context. That must read as `canceled` — filing it
// as a provider failure would blame the SMTP server for every deploy.
func TestSend_CancellationIsClassifiedAsCanceledNotAsAProviderFailure(t *testing.T) {
	o, _ := testOutbox(t)
	m := &fakeMailer{err: context.Canceled}
	w := newTestWorker(t, o, m)

	reason, fatal, err := w.send(seal(t, o, mailer.TemplateLoginCode, "en",
		loginCodeParams()))

	require.Error(t, err)
	require.False(t, fatal)
	require.Equal(t, "canceled", reason)
}

// loginCodeParams is every param the login_code catalogue references. Templates
// execute with missingkey=error, so a partial set is a render failure rather
// than a message with a blank in it.
func loginCodeParams() map[string]string {
	return map[string]string{mailer.ParamCode: "123456", mailer.ParamExpiresMinutes: "10"}
}

package mailoutbox

import (
	"crypto/rand"
	"encoding/json"
	"math"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
)

func TestTopology_DefaultsFillOnlyWhatIsUnset(t *testing.T) {
	full := Topology{Exchange: "x", Queue: "q", RoutingKey: "k"}.WithDefaults()
	require.Equal(t, Topology{Exchange: "x", Queue: "q", RoutingKey: "k"}, full)

	empty := Topology{}.WithDefaults()
	require.Equal(t, DefaultExchange, empty.Exchange)
	require.Equal(t, DefaultQueue, empty.Queue)
	require.Equal(t, DefaultRoutingKey, empty.RoutingKey)
	require.Equal(t, DefaultQueue, Topology{}.QueueName())
}

// The ladder is what turns "retry" into "retry later, and later still". A step
// that fell off the end would panic on the index or, worse, silently reuse the
// first rung and retry a dead SMTP server every minute forever.
func TestTopology_RetryLadderSaturatesInsteadOfRunningOff(t *testing.T) {
	tp := Topology{}.WithDefaults()
	require.Equal(t, DefaultExchange+".retry.1m", tp.retryKey(0))
	require.Equal(t, DefaultExchange+".retry.5m", tp.retryKey(1))
	require.Equal(t, DefaultExchange+".retry.30m", tp.retryKey(2))

	// Past the end and before the beginning both clamp rather than explode.
	require.Equal(t, DefaultExchange+".retry.30m", tp.retryKey(3))
	require.Equal(t, DefaultExchange+".retry.30m", tp.retryKey(99))
	require.Equal(t, DefaultExchange+".retry.1m", tp.retryKey(-1))
}

// AMQP clients disagree about which integer width a small number is, and the
// count that decides whether a reset link is abandoned must not depend on the
// disagreement.
func TestAttempt_ReadsEveryIntegerWidthAndDefaultsToZero(t *testing.T) {
	for name, headers := range map[string]amqp.Table{
		"int32": {AttemptHeader: int32(3)},
		"int64": {AttemptHeader: int64(3)},
		"int":   {AttemptHeader: 3},
		"int16": {AttemptHeader: int16(3)},
		"int8":  {AttemptHeader: int8(3)},
	} {
		t.Run(name, func(t *testing.T) { require.Equal(t, 3, Attempt(headers)) })
	}

	require.Equal(t, 0, Attempt(nil))
	require.Equal(t, 0, Attempt(amqp.Table{}))
	// A header of the wrong type is treated as a first attempt rather than
	// panicking: the message still gets its full ladder, which is the safe
	// direction for something a user is waiting on.
	require.Equal(t, 0, Attempt(amqp.Table{AttemptHeader: "three"}))
}

// The counter is ours, but it arrives on a header anyone with publish rights on
// a shared broker can write. The worker adds one to whatever it reports, and
// without a ceiling math.MaxInt64 wraps that to a negative number: the give-up
// test reads it as an early attempt, the ladder clamps to its slowest rung, and
// the value written back truncates to zero through the int32 header. Nothing
// crashes and nothing is logged — the message simply circles forever instead of
// reaching the dead queue, which for this worker means a sign-in code that is
// never delivered and never given up on.
func TestAttempt_IsBoundedSoTheGiveUpTestCannotBeWrappedPastIt(t *testing.T) {
	require.Equal(t, attemptCeiling, Attempt(amqp.Table{AttemptHeader: int64(math.MaxInt64)}))
	require.Equal(t, attemptCeiling, Attempt(amqp.Table{AttemptHeader: int32(math.MaxInt32)}))
	require.Equal(t, attemptCeiling, Attempt(amqp.Table{AttemptHeader: attemptCeiling + 1}))

	// Negatives clamp up rather than through: a message must not buy itself
	// extra attempts by arriving with a counter below zero.
	require.Equal(t, 0, Attempt(amqp.Table{AttemptHeader: int64(math.MinInt64)}))
	require.Equal(t, 0, Attempt(amqp.Table{AttemptHeader: int32(-7)}))
	require.Equal(t, 0, Attempt(amqp.Table{AttemptHeader: int8(-1)}))

	// The bound is far above any real ladder, so ordinary counters pass through
	// untouched — a ceiling that clipped real attempts would be its own bug.
	require.Equal(t, 4, Attempt(amqp.Table{AttemptHeader: int64(4)}))
	require.Equal(t, int32(attemptCeiling), clampAttempt(attemptCeiling))

	// What the clamp is actually protecting: the +1 the worker performs, and the
	// int32 the header is written as. Both survive the ceiling; neither survives
	// an unclamped MaxInt64.
	require.Positive(t, Attempt(amqp.Table{AttemptHeader: int64(math.MaxInt64)})+1)
	require.Equal(t, attemptCeiling,
		int(int32(Attempt(amqp.Table{AttemptHeader: int64(math.MaxInt64)}))))
}

func TestWire_RoundTripsAndRefusesAnIncompleteMessage(t *testing.T) {
	body, err := encodeWire(Outgoing{
		ID: 42, Template: mailer.TemplatePasswordReset, Recipient: "grace@x.test",
		Locale: "pt", Ciphertext: []byte{1, 2, 3}, Nonce: []byte{4, 5},
	})
	require.NoError(t, err)

	got, err := DecodeWire(body)
	require.NoError(t, err)
	require.Equal(t, int64(42), got.OutboxID)
	require.Equal(t, mailer.TemplatePasswordReset, got.Template)
	require.Equal(t, "grace@x.test", got.Recipient)
	require.Equal(t, "pt", got.Locale)
	require.Equal(t, []byte{1, 2, 3}, got.Ciphertext)

	_, err = DecodeWire([]byte("not json"))
	require.Error(t, err)

	// A message with no template or no recipient cannot be sent, and accepting
	// it would only move the failure to the render step where it looks like a
	// template bug instead of a truncated payload.
	_, err = DecodeWire([]byte(`{"recipient":"a@b.test"}`))
	require.Error(t, err)
	_, err = DecodeWire([]byte(`{"template":"password_reset"}`))
	require.Error(t, err)
}

func TestOpenWire_ReversesTheSealAndFailsOnATamperedPayload(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	o, err := NewFromMasterKey(key)
	require.NoError(t, err)

	sealed := sealForTest(t, o, map[string]string{"Link": "https://x.test/#reset=abc"})
	msg := WireMessage{
		Template:   mailer.TemplatePasswordReset,
		Recipient:  "grace@x.test",
		Locale:     "en",
		Ciphertext: sealed.Ciphertext,
		Nonce:      sealed.Nonce,
	}

	env, err := o.OpenWire(msg)
	require.NoError(t, err)
	require.Equal(t, "https://x.test/#reset=abc", env.Params["Link"])
	require.Equal(t, "grace@x.test", env.To)

	// The GCM tag is the point: write access to the broker or the table must
	// not be able to swap the link for another one.
	tampered := msg
	tampered.Ciphertext = append([]byte(nil), msg.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xFF
	_, err = o.OpenWire(tampered)
	require.Error(t, err)
}

func TestNewAMQPSink_RefusesAConfigWithNoBroker(t *testing.T) {
	_, err := NewAMQPSink(AMQPConfig{})
	require.ErrorIs(t, err, ErrNoBrokerURL)

	_, err = Dial(AMQPConfig{})
	require.ErrorIs(t, err, ErrNoBrokerURL)

	s, err := NewAMQPSink(AMQPConfig{URL: "amqp://localhost:5672/"})
	require.NoError(t, err)
	require.Equal(t, "amqp", s.Name())
	// Defaults are applied at construction so a zero Topology still names real
	// queues rather than publishing to "".
	require.Equal(t, DefaultExchange, s.cfg.Topology.Exchange)
	require.NoError(t, s.Close())
	// Close is idempotent — shutdown may reach it after an error path already did.
	require.NoError(t, s.Close())
}

// dialAMQP must never echo the URL back, because the URL carries the broker
// password and an error string is the one place credentials escape redaction
// (logsafe blanks ATTRIBUTES, never the message).
func TestDialAMQP_DoesNotEchoTheBrokerCredential(t *testing.T) {
	_, err := dialAMQP("amqp://user:hunter2@%zz/", nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "hunter2")
}

// sealForTest encrypts params through the outbox's own cipher, so the test
// exercises the real key derivation rather than a hand-rolled stand-in.
func sealForTest(t *testing.T, o *Outbox, params map[string]string) Outgoing {
	t.Helper()
	plain, err := json.Marshal(params)
	require.NoError(t, err)
	ct, nonce, err := o.cipher.Encrypt(plain)
	require.NoError(t, err)
	return Outgoing{Ciphertext: ct, Nonce: nonce}
}

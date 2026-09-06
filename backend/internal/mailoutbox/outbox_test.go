package mailoutbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
	"foldex/internal/pkg/secrets"
)

func testCipher(t *testing.T, seed byte) *secrets.Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	c, err := secrets.NewCipher(key)
	require.NoError(t, err)
	return c
}

func TestNewRefusesAMissingCipher(t *testing.T) {
	t.Parallel()
	_, err := New(nil)
	assert.ErrorIs(t, err, ErrNoCipher)
}

// The params carry a live reset link, so what lands in the row must not be
// readable. This is the same property that makes password_reset store a sha256
// instead of the token.
func TestOpenReversesTheEncryptionAndNothingElseDoes(t *testing.T) {
	t.Parallel()
	o, err := New(testCipher(t, 1))
	require.NoError(t, err)

	env := mailer.PasswordResetMessage("user@example.com", "https://foldex.test/#reset=SECRET-TOKEN", 30)
	plain, err := json.Marshal(env.Params)
	require.NoError(t, err)
	ct, nonce, err := o.cipher.Encrypt(plain)
	require.NoError(t, err)

	assert.False(t, bytes.Contains(ct, []byte("SECRET-TOKEN")),
		"the reset token is readable in the stored ciphertext")

	got, err := o.Open(Outgoing{
		Template: env.Template, Recipient: env.To, Ciphertext: ct, Nonce: nonce,
	})
	require.NoError(t, err)
	assert.Equal(t, env.Params[mailer.ParamActionURL], got.Params[mailer.ParamActionURL])
	assert.Equal(t, env.Template, got.Template)
	assert.Equal(t, env.To, got.To)

	// A different key must not open it. Losing AUTH_ENCRYPTION_KEY strands the
	// queue, which is the same trade the TOTP seed already makes.
	other, err := New(testCipher(t, 9))
	require.NoError(t, err)
	_, err = other.Open(Outgoing{Ciphertext: ct, Nonce: nonce})
	assert.ErrorIs(t, err, secrets.ErrDecrypt)
}

// The authentication tag is why this is GCM and not CTR: someone with write
// access to the row must not be able to repoint a recovery link.
func TestOpenRejectsATamperedPayload(t *testing.T) {
	t.Parallel()
	o, err := New(testCipher(t, 2))
	require.NoError(t, err)
	ct, nonce, err := o.cipher.Encrypt([]byte(`{"ActionURL":"https://foldex.test/#reset=T"}`))
	require.NoError(t, err)

	tampered := append([]byte(nil), ct...)
	tampered[0] ^= 0x01
	_, err = o.Open(Outgoing{Ciphertext: tampered, Nonce: nonce})
	assert.ErrorIs(t, err, secrets.ErrDecrypt)
}

func TestEnqueueRefusesAnIncompleteEnvelope(t *testing.T) {
	t.Parallel()
	o, err := New(testCipher(t, 3))
	require.NoError(t, err)
	for _, env := range []mailer.Envelope{
		{To: "a@b.c"},
		{Template: mailer.TemplateInvite},
	} {
		// nil tx is never reached: validation runs first, which is the point.
		err := o.EnqueueTx(context.Background(), nil, env, "en")
		assert.Error(t, err)
	}
}

func TestFailureReasonNeverEchoesTheTransportsText(t *testing.T) {
	t.Parallel()
	secret := "550 rejected recipient victim@example.com"
	cases := map[string]error{
		"canceled":              context.Canceled,
		"timeout":               context.DeadlineExceeded,
		"unknown_template":      mailer.ErrUnknownTemplate,
		"undecryptable_payload": secrets.ErrDecrypt,
		"send_failed":           errors.New(secret),
	}
	for want, err := range cases {
		got := failureReason(err)
		assert.Equal(t, want, got)
		assert.NotContains(t, got, "victim@example.com")
		assert.False(t, strings.Contains(got, "550"))
	}
}

// A permanent failure has to be distinguishable from a transient one, or a
// message naming a deleted template retries for hours before anyone sees it.
func TestPermanentWrapsAndStaysIdentifiable(t *testing.T) {
	t.Parallel()
	err := permanent(mailer.ErrUnknownTemplate)
	assert.ErrorIs(t, err, ErrPermanent)
	assert.ErrorIs(t, err, mailer.ErrUnknownTemplate)
	assert.False(t, errors.Is(errors.New("transient"), ErrPermanent))
}

func TestBackoffGrowsAndIsBounded(t *testing.T) {
	t.Parallel()
	var prev = backoff(0)
	for attempt := 1; attempt <= 8; attempt++ {
		d := backoff(attempt)
		assert.GreaterOrEqual(t, d, prev, "backoff must not shrink at attempt %d", attempt)
		assert.LessOrEqual(t, d.Hours(), 1.0, "backoff must stay bounded")
		prev = d
	}
}

func TestIntervalArgIsAPostgresLiteral(t *testing.T) {
	t.Parallel()
	// Go's own duration form (`1h5m0s`) is not an interval Postgres parses, and
	// the failure would only surface against a real database.
	assert.Equal(t, "60 seconds", intervalArg(time.Minute))
	assert.Equal(t, "0 seconds", intervalArg(-time.Minute))
}

func TestRelay_PanicOnOneMessageDoesNotAckOrKillTheProcess(t *testing.T) {
	q := &fakeQueue{claimed: []Outgoing{
		{ID: 1, ClaimToken: "tok-1"},
		{ID: 2, ClaimToken: "tok-2"},
	}}
	sink := &panicThenOKSink{panicID: 1}
	var logs bytes.Buffer
	rl := NewRelay(nil, sink, Options{Workers: 1, Batch: 8, PollInterval: time.Hour},
		slog.New(slog.NewTextHandler(&logs, nil)))
	rl.repo = q
	rl.ctx = context.Background()

	assert.False(t, rl.drain())

	assert.Equal(t, []int64{2}, sink.snapshot())
	assert.Equal(t, []int64{2}, q.snapshotPublished())
	assert.Empty(t, q.snapshotFailed(), "a panic is not a delivery failure to settle")
	assert.Contains(t, logs.String(), "mail boom")
	assert.Contains(t, logs.String(), "panicked")
}

type fakeQueue struct {
	mu        sync.Mutex
	claimed   []Outgoing
	published []int64
	failed    []int64
}

func (q *fakeQueue) Claim(_ context.Context, n int) ([]Outgoing, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if n > len(q.claimed) {
		n = len(q.claimed)
	}
	out := append([]Outgoing(nil), q.claimed[:n]...)
	q.claimed = q.claimed[n:]
	return out, nil
}

func (q *fakeQueue) MarkPublished(_ context.Context, id int64, _ string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.published = append(q.published, id)
	return nil
}

func (q *fakeQueue) MarkFailed(_ context.Context, id int64, _ string, _ string, _ time.Duration, _ int, _ bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failed = append(q.failed, id)
	return nil
}

func (*fakeQueue) RequeueStuck(context.Context, time.Duration) (int64, error) { return 0, nil }
func (*fakeQueue) Purge(context.Context, time.Duration, time.Duration) (int64, error) {
	return 0, nil
}

func (q *fakeQueue) snapshotPublished() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]int64(nil), q.published...)
}

func (q *fakeQueue) snapshotFailed() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]int64(nil), q.failed...)
}

type panicThenOKSink struct {
	mu      sync.Mutex
	panicID int64
	got     []int64
}

func (*panicThenOKSink) Name() string { return "test" }

func (s *panicThenOKSink) Deliver(_ context.Context, m Outgoing) error {
	if m.ID == s.panicID {
		panic("mail boom")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, m.ID)
	return nil
}

func (s *panicThenOKSink) snapshot() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.got...)
}

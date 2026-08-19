package mailoutbox

import (
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	"foldex/internal/mailer"
)

// Default topology names. They are configurable so a shared broker can host
// more than one foldex instance, but the SHAPE is not: the relay publishes and
// the worker consumes against the same declarations, and a mismatch in the
// arguments below is a channel-level PRECONDITION_FAILED at boot rather than a
// silent behaviour change.
const (
	DefaultExchange   = "foldex.mail"
	DefaultQueue      = "foldex.mail.send"
	DefaultRoutingKey = "send"

	dlxSuffix  = ".dlx"
	deadSuffix = ".dead"
)

// retryLadder is the backoff, expressed as one queue per step.
//
// One queue per step rather than a per-message TTL on a single queue, which is
// the obvious shortcut and the wrong one: RabbitMQ expires messages from the
// HEAD only, so a message sitting in front with a 30-minute TTL holds back
// every shorter one queued behind it. The ladder costs three declarations and
// makes each step's wait exactly what it says.
var retryLadder = []struct {
	suffix string
	ttlMS  int32
}{
	{".retry.1m", 60_000},
	{".retry.5m", 300_000},
	{".retry.30m", 1_800_000},
}

// Topology is the exchange/queue naming for one foldex instance.
type Topology struct {
	Exchange   string
	Queue      string
	RoutingKey string
}

// WithDefaults fills the unset names. Callers outside this package need it
// because a zero Topology is the normal case: an instance that does not share
// its broker configures nothing.
func (t Topology) WithDefaults() Topology {
	if t.Exchange == "" {
		t.Exchange = DefaultExchange
	}
	if t.Queue == "" {
		t.Queue = DefaultQueue
	}
	if t.RoutingKey == "" {
		t.RoutingKey = DefaultRoutingKey
	}
	return t
}

func (t Topology) dlx() string       { return t.Exchange + dlxSuffix }
func (t Topology) deadQueue() string { return t.Exchange + deadSuffix }

// QueueName is the send queue, defaults applied. cmd/mailer consumes it and
// lives outside this package.
func (t Topology) QueueName() string { return t.WithDefaults().Queue }

// retryKey names the ladder step for an attempt, saturating at the last one.
func (t Topology) retryKey(attempt int) string {
	i := attempt
	if i < 0 {
		i = 0
	}
	if i >= len(retryLadder) {
		i = len(retryLadder) - 1
	}
	return t.Exchange + retryLadder[i].suffix
}

// Declare builds the whole topology. It is idempotent and both the relay and
// the worker call it on every connect, so neither depends on the other having
// started first — a worker booting against an undeclared exchange would
// otherwise fail in a way that looks like a broker outage.
func (t Topology) Declare(ch *amqp.Channel) error {
	t = t.WithDefaults()
	for _, name := range []string{t.Exchange, t.dlx()} {
		if err := ch.ExchangeDeclare(name, amqp.ExchangeDirect, true, false, false, false, nil); err != nil {
			return fmt.Errorf("mailoutbox: declare exchange %s: %w", name, err)
		}
	}

	// Quorum queues, not classic mirrored ones: this queue holds sign-in codes
	// and reset links, and a classic queue can lose confirmed messages on a
	// failover — which would make the publisher confirm the relay relies on a
	// promise the broker cannot keep.
	quorum := func(extra amqp.Table) amqp.Table {
		args := amqp.Table{"x-queue-type": "quorum"}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}

	if _, err := ch.QueueDeclare(t.Queue, true, false, false, false, quorum(amqp.Table{
		"x-dead-letter-exchange": t.dlx(),
	})); err != nil {
		return fmt.Errorf("mailoutbox: declare queue %s: %w", t.Queue, err)
	}
	if err := ch.QueueBind(t.Queue, t.RoutingKey, t.Exchange, false, nil); err != nil {
		return fmt.Errorf("mailoutbox: bind queue %s: %w", t.Queue, err)
	}

	for _, step := range retryLadder {
		name := t.Exchange + step.suffix
		// Expiry sends the message back to the MAIN exchange, so a retry is an
		// ordinary delivery again rather than a special case the worker has to
		// recognise.
		if _, err := ch.QueueDeclare(name, true, false, false, false, quorum(amqp.Table{
			"x-message-ttl":             step.ttlMS,
			"x-dead-letter-exchange":    t.Exchange,
			"x-dead-letter-routing-key": t.RoutingKey,
		})); err != nil {
			return fmt.Errorf("mailoutbox: declare retry queue %s: %w", name, err)
		}
		if err := ch.QueueBind(name, name, t.dlx(), false, nil); err != nil {
			return fmt.Errorf("mailoutbox: bind retry queue %s: %w", name, err)
		}
	}

	// No TTL on the dead queue. It is the operator's inbox for messages that
	// exhausted the ladder, and a queue that quietly drops them would remove
	// the only place the failure is still visible.
	if _, err := ch.QueueDeclare(t.deadQueue(), true, false, false, false, quorum(nil)); err != nil {
		return fmt.Errorf("mailoutbox: declare dead queue: %w", err)
	}
	if err := ch.QueueBind(t.deadQueue(), t.deadQueue(), t.dlx(), false, nil); err != nil {
		return fmt.Errorf("mailoutbox: bind dead queue: %w", err)
	}
	return nil
}

// Republish sends a body back into the DLX, either onto a ladder step or into
// the dead queue.
//
// The worker republishes explicitly instead of nacking, because a nack routes
// by the queue's own dead-letter-routing-key — one fixed destination, which
// cannot express "wait a minute this time, half an hour the next". The cost is
// that a crash between the publish and the ack redelivers the message; for a
// send that already FAILED, retrying it once more is the harmless direction.
//
// It goes through ConfirmingChannel rather than publishing directly, and that
// is load-bearing: the caller ACKs the original delivery on success, so a
// publish that was confirmed-but-returned would delete the only copy of a
// message no queue ever accepted.
func (t Topology) Republish(ctx context.Context, ch *ConfirmingChannel,
	body []byte, attempt int, reason string, dead bool) error {

	t = t.WithDefaults()
	// Clamped again at the boundary, not only where the counter is read. The
	// header is int32 on the wire and the caller's `attempt` is an int, so the
	// conversion is where a hostile or simply wrong value stops being detectable
	// — it truncates silently, and a truncated counter restarts the ladder.
	bounded := clampAttempt(int64(attempt))
	key := t.deadQueue()
	if !dead {
		key = t.retryKey(int(bounded) - 1)
	}
	if err := ch.Publish(ctx, t.dlx(), key, amqp.Table{
		AttemptHeader: bounded,
		ReasonHeader:  reason,
	}, body); err != nil {
		return fmt.Errorf("mailoutbox: republish: %w", err)
	}
	return nil
}

// attemptCeiling bounds the retry counter, in both directions.
//
// The counter is ours and stays in single digits, but it travels as a header on
// a broker this application's threat model deliberately excludes: it persists to
// disk and is routinely shared between projects, so anyone with publish rights
// writes whatever integer they like there. The worker then computes
// `Attempt(headers) + 1`, and math.MaxInt64 wraps that to a NEGATIVE number —
// which reads as "attempt 1" to the give-up test, clamps onto the slowest ladder
// step, and is written back truncated to zero. The message then retries forever
// instead of reaching the dead queue: no crash, no log, just a sign-in code
// circling every thirty minutes in a worker whose whole job is to deliver it.
//
// The exact ceiling does not matter as long as it is far above MaxAttempts and
// inside int32 — every value at or over the give-up threshold already behaves
// identically, so clamping discards nothing real.
const attemptCeiling = 1 << 20

// Attempt reads the retry counter off a delivery, tolerating every integer
// width an AMQP client may have encoded it as, and refusing to report a value
// arithmetic downstream cannot survive.
func Attempt(headers amqp.Table) int {
	var n int64
	switch v := headers[AttemptHeader].(type) {
	case int32:
		n = int64(v)
	case int64:
		n = v
	case int:
		n = int64(v)
	case int16:
		n = int64(v)
	case int8:
		n = int64(v)
	default:
		return 0
	}
	return int(clampAttempt(n))
}

// clampAttempt is the one place the counter is bounded, and it returns the
// header's own width. Returning int and converting at the call site left the
// bound one function call away from the conversion — true at runtime, but not
// visible to a reader (or to gosec) looking at `int32(attempt)` in isolation.
// The comparison against the ceiling sits directly above the conversion so the
// two cannot drift apart.
func clampAttempt(n int64) int32 {
	if n < 0 {
		return 0
	}
	if n > attemptCeiling {
		return attemptCeiling
	}
	return int32(n)
}

// AttemptHeader carries the retry count the worker maintains itself.
//
// Deliberately not x-death: its semantics differ between queue types and
// between broker versions, and the count that decides whether a reset link is
// abandoned should not depend on either.
const AttemptHeader = "x-foldex-attempt"

// ReasonHeader carries the normalized failure reason to the dead queue, so the
// backend can settle the outbox row without ever opening the payload.
const ReasonHeader = "x-foldex-reason"

// WireMessage is what travels to the broker.
//
// The payload stays SEALED: this is the same AES-256-GCM ciphertext the outbox
// row holds, and the worker is the only process that opens it. A broker
// persists messages to disk and is frequently shared between projects, so a
// rendered reset link on the wire would put a live credential in a store that
// is neither this application's nor covered by its threat model.
//
// The recipient travels in clear, which is a deliberate and narrower exposure:
// the outbox row already stores it that way, the worker needs it to address the
// message, and it is personal data rather than a credential. Encrypting it here
// alone would buy nothing while the table beside it does not.
type WireMessage struct {
	OutboxID   int64  `json:"outbox_id"`
	Template   string `json:"template"`
	Recipient  string `json:"recipient"`
	Locale     string `json:"locale"`
	Ciphertext []byte `json:"ciphertext"`
	Nonce      []byte `json:"nonce"`
}

func encodeWire(m Outgoing) ([]byte, error) {
	body, err := json.Marshal(WireMessage{
		OutboxID:   m.ID,
		Template:   m.Template,
		Recipient:  m.Recipient,
		Locale:     m.Locale,
		Ciphertext: m.Ciphertext,
		Nonce:      m.Nonce,
	})
	if err != nil {
		return nil, fmt.Errorf("mailoutbox: encode wire message: %w", err)
	}
	return body, nil
}

// DecodeWire parses a delivery body. It is exported because cmd/mailer is the
// consumer and lives outside this package.
func DecodeWire(body []byte) (WireMessage, error) {
	var m WireMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return WireMessage{}, fmt.Errorf("mailoutbox: decode wire message: %w", err)
	}
	if m.Template == "" || m.Recipient == "" {
		return WireMessage{}, fmt.Errorf("mailoutbox: wire message is missing a template or recipient")
	}
	return m, nil
}

// OpenWire reverses the outbox encryption for a message that arrived over the
// wire, reusing the same cipher the relay sealed it with. The locale stays on
// the WireMessage — it is routing information for the renderer, not payload.
func (o *Outbox) OpenWire(m WireMessage) (mailer.Envelope, error) {
	return o.Open(Outgoing{
		Template:   m.Template,
		Recipient:  m.Recipient,
		Ciphertext: m.Ciphertext,
		Nonce:      m.Nonce,
	})
}

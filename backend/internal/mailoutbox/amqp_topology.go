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
	key := t.deadQueue()
	if !dead {
		key = t.retryKey(attempt - 1)
	}
	if err := ch.Publish(ctx, t.dlx(), key, amqp.Table{
		AttemptHeader: int32(attempt),
		ReasonHeader:  reason,
	}, body); err != nil {
		return fmt.Errorf("mailoutbox: republish: %w", err)
	}
	return nil
}

// Attempt reads the retry counter off a delivery, tolerating every integer
// width an AMQP client may have encoded it as.
func Attempt(headers amqp.Table) int {
	switch v := headers[AttemptHeader].(type) {
	case int32:
		return int(v)
	case int64:
		return int(v)
	case int:
		return v
	case int16:
		return int(v)
	case int8:
		return int(v)
	default:
		return 0
	}
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

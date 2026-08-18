package mailoutbox

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ErrUnroutable means the broker accepted a message that no queue would take.
//
// This is a distinct failure from a nack and it is the one that is easy to miss:
// a publisher confirm answers "the exchange finished processing this", NOT "a
// queue holds it". A `mandatory` message with no matching binding is RETURNED
// and then CONFIRMED, so code that only waits for the confirm sees success and
// throws the message away — the relay marks the row published, or the worker
// acks the original delivery, and a live reset link exists nowhere.
var ErrUnroutable = errors.New("mailoutbox: message was not routable")

// ConfirmingChannel is an AMQP channel that reports a publish as successful
// only when the broker both confirmed it and did not return it.
//
// It is the single publish path for this package: the relay's sink and the
// worker's retry ladder both go through it, so neither can forget the return
// half of the contract.
type ConfirmingChannel struct {
	ch      *amqp.Channel
	returns chan amqp.Return
}

// NewConfirmingChannel opens a channel, puts it in confirm mode and starts
// observing returns.
func NewConfirmingChannel(conn *amqp.Connection) (*ConfirmingChannel, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("mailoutbox: open channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("mailoutbox: enable publisher confirms: %w", err)
	}
	// Buffered so the connection's reader goroutine never blocks handing us a
	// return. It must not block: the very next frame it has to deliver is the
	// confirm this publish is waiting on, so a full channel here would deadlock
	// the publish against its own return.
	return &ConfirmingChannel{ch: ch, returns: ch.NotifyReturn(make(chan amqp.Return, 8))}, nil
}

// Raw exposes the underlying channel for declarations. Publishing through it
// directly bypasses the return check and must not be done.
func (c *ConfirmingChannel) Raw() *amqp.Channel { return c.ch }

func (c *ConfirmingChannel) Close() error {
	if c == nil || c.ch == nil {
		return nil
	}
	return c.ch.Close()
}

func (c *ConfirmingChannel) closed() bool { return c == nil || c.ch == nil || c.ch.IsClosed() }

// Publish sends one message and waits for the broker to take responsibility.
//
// Ordering is what makes the return check reliable rather than racy: RabbitMQ
// emits basic.return BEFORE the basic.ack for the same message, and amqp091
// dispatches both from the same connection reader in frame order. So by the
// time the confirm resolves, a return for this publish is already sitting in
// the buffer, and a non-blocking read after the confirm cannot miss it.
//
// Publishes must be serialized by the caller — amqp091 channels are not safe
// for concurrent use, and the reasoning above assumes one publish in flight.
func (c *ConfirmingChannel) Publish(ctx context.Context, exchange, key string,
	headers amqp.Table, body []byte) error {

	// A return left over from an earlier publish would be misread as this one's.
	// Nothing should be here — every Publish drains its own — but a stale entry
	// would attribute a past failure to a message that actually succeeded.
	c.drain()

	conf, err := c.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, true, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
			Headers:      headers,
		})
	if err != nil {
		return fmt.Errorf("mailoutbox: publish: %w", err)
	}
	ok, err := conf.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("mailoutbox: await confirm: %w", err)
	}
	if !ok {
		// A nack means the broker refused responsibility. Retrying is right: the
		// usual causes are a full disk or a queue mid-redeclaration, and both
		// recover.
		return errors.New("mailoutbox: broker nacked the message")
	}
	select {
	case r := <-c.returns:
		// Never include the body: it is the sealed payload, and the reply text
		// comes from the broker.
		return fmt.Errorf("%w: exchange %q has nothing bound to %q (reply %d)",
			ErrUnroutable, r.Exchange, r.RoutingKey, r.ReplyCode)
	default:
	}
	return nil
}

// ReconnectWait spreads redials out.
//
// A fixed delay is fine for one process and wrong for several: a broker restart
// drops every replica's connection at the same instant, so every one of them
// would redial in lockstep, every 5 seconds, for as long as the outage lasts.
// Up to 20% of jitter is enough to break the convoy without making the recovery
// noticeably slower.
//
// crypto/rand rather than math/rand, even though nothing here is a secret and
// scheduling jitter genuinely does not need unpredictability. The reason is
// cost, not threat: this runs once per reconnect attempt on a path that is
// about to sleep for seconds and open a socket, so the difference is
// unmeasurable — and using the audited source means no standing lint exception
// that a future reader has to re-derive as safe. A failure falls back to the
// base delay: losing jitter is a worse convoy, never a wrong wait.
func ReconnectWait(base time.Duration) time.Duration {
	spread := int64(base/5) + 1
	n, err := rand.Int(rand.Reader, big.NewInt(spread))
	if err != nil {
		return base
	}
	return base + time.Duration(n.Int64())
}

func (c *ConfirmingChannel) drain() {
	for {
		select {
		case <-c.returns:
		default:
			return
		}
	}
}

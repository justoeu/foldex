//go:build integration

package mailworker

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"foldex/internal/mailer"
	"foldex/internal/mailoutbox"
)

const rabbitImage = "rabbitmq:4.3.2-alpine"

func startRabbit(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        rabbitImage,
			ExposedPorts: []string{"5672/tcp"},
			WaitingFor: wait.ForLog("Server startup complete").
				WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "5672/tcp")
	require.NoError(t, err)
	return "amqp://guest:guest@" + host + ":" + port.Port() + "/"
}

// brokerFixture returns a live broker plus the pieces both sides share.
func brokerFixture(t *testing.T) (string, *mailoutbox.Outbox, mailoutbox.Topology, *mailoutbox.AMQPSink) {
	t.Helper()
	url := startRabbit(t)
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	o, err := mailoutbox.NewFromMasterKey(key)
	require.NoError(t, err)
	tp := mailoutbox.Topology{}.WithDefaults()
	sink, err := mailoutbox.NewAMQPSink(mailoutbox.AMQPConfig{URL: url, Topology: tp})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sink.Close() })
	return url, o, tp, sink
}

func publish(t *testing.T, sink *mailoutbox.AMQPSink, o *mailoutbox.Outbox, template string, params map[string]string) {
	t.Helper()
	ct, nonce, err := o.Seal(params)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, sink.Deliver(ctx, mailoutbox.Outgoing{
		ID: 1, Template: template, Recipient: "grace@x.test", Locale: "en",
		Ciphertext: ct, Nonce: nonce,
	}))
}

func queueDepth(t *testing.T, url, name string) int {
	t.Helper()
	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	require.NoError(t, err)
	defer func() { _ = ch.Close() }()
	q, err := ch.QueueInspect(name)
	require.NoError(t, err)
	return q.Messages
}

// The happy path end to end: the relay publishes a sealed message, the worker
// consumes it, opens it and sends it. Nothing is left on any queue.
func TestWorker_ConsumesSendsAndAcks(t *testing.T) {
	url, o, tp, sink := brokerFixture(t)
	m := &fakeMailer{}

	w := New(o, m, mailoutbox.AMQPConfig{URL: url, Topology: tp}, Options{}, discard())
	w.Start(context.Background())
	defer w.Stop()

	publish(t, sink, o, mailer.TemplateLoginCode, loginCodeParams())

	require.Eventually(t, func() bool { return len(m.delivered()) == 1 },
		30*time.Second, 100*time.Millisecond, "the worker should deliver the message")
	require.Eventually(t, func() bool { return queueDepth(t, url, tp.QueueName()) == 0 },
		30*time.Second, 200*time.Millisecond, "a delivered message must be acked off the queue")
	require.Equal(t, "grace@x.test", m.delivered()[0].To)
}

// A transport failure must land on the FIRST rung, not in the dead queue and
// not back on the send queue for an immediate hot retry.
func TestWorker_ARetryableFailureLandsOnTheFirstLadderRung(t *testing.T) {
	url, o, tp, sink := brokerFixture(t)
	m := &fakeMailer{err: errors.New("smtp is down")}

	w := New(o, m, mailoutbox.AMQPConfig{URL: url, Topology: tp}, Options{}, discard())
	w.Start(context.Background())
	defer w.Stop()

	publish(t, sink, o, mailer.TemplateLoginCode, loginCodeParams())

	require.Eventually(t, func() bool {
		return queueDepth(t, url, tp.Exchange+".retry.1m") == 1
	}, 30*time.Second, 200*time.Millisecond, "attempt 1 belongs on the 1-minute rung")
	require.Equal(t, 0, queueDepth(t, url, tp.QueueName()))
	require.Equal(t, 0, queueDepth(t, url, tp.Exchange+".dead"))
}

// A permanent failure skips the whole ladder. Spending 36 minutes retrying a
// payload that will never decrypt only delays the operator finding out.
func TestWorker_APermanentFailureGoesStraightToTheDeadQueue(t *testing.T) {
	url, o, tp, sink := brokerFixture(t)
	m := &fakeMailer{}

	w := New(o, m, mailoutbox.AMQPConfig{URL: url, Topology: tp}, Options{}, discard())
	w.Start(context.Background())
	defer w.Stop()

	// A template this binary does not ship.
	publish(t, sink, o, "no_such_template", nil)

	require.Eventually(t, func() bool {
		return queueDepth(t, url, tp.Exchange+".dead") == 1
	}, 30*time.Second, 200*time.Millisecond, "an unknown template must not ride the ladder")
	require.Equal(t, 0, queueDepth(t, url, tp.Exchange+".retry.1m"))
	require.Empty(t, m.delivered())
}

// Once the budget is spent the message stops circulating and becomes the
// operator's problem — which is what the backend's watcher then reports.
func TestWorker_GivesUpOntoTheDeadQueueWhenTheBudgetIsSpent(t *testing.T) {
	url, o, tp, sink := brokerFixture(t)
	m := &fakeMailer{err: errors.New("smtp is down")}

	// MaxAttempts=1 makes the very first failure the last one, without waiting
	// out three real TTLs.
	w := New(o, m, mailoutbox.AMQPConfig{URL: url, Topology: tp},
		Options{MaxAttempts: 1}, discard())
	w.Start(context.Background())
	defer w.Stop()

	publish(t, sink, o, mailer.TemplateLoginCode, loginCodeParams())

	require.Eventually(t, func() bool {
		return queueDepth(t, url, tp.Exchange+".dead") == 1
	}, 30*time.Second, 200*time.Millisecond)
	require.Equal(t, 0, queueDepth(t, url, tp.Exchange+".retry.1m"))
}

// A crafted attempt header must not buy a message an unbounded life.
//
// The counter travels on a broker this application's threat model excludes, so
// anyone with publish rights writes what they like there. `Attempt(headers)+1`
// on math.MaxInt64 wraps NEGATIVE: the give-up test reads it as an early
// attempt, the ladder clamps to its slowest rung, and the value written back
// truncates to zero through the int32 header — so the next round starts over at
// one. Nothing crashes and nothing is logged; the message circles every thirty
// minutes forever instead of reaching the operator's inbox.
func TestWorker_ACraftedAttemptHeaderStillReachesTheDeadQueue(t *testing.T) {
	url, o, tp, _ := brokerFixture(t)
	m := &fakeMailer{err: errors.New("smtp is down")}

	w := New(o, m, mailoutbox.AMQPConfig{URL: url, Topology: tp},
		Options{MaxAttempts: 3}, discard())
	w.Start(context.Background())
	defer w.Stop()

	ct, nonce, err := o.Seal(loginCodeParams())
	require.NoError(t, err)
	// Marshalled here rather than through the sink: the sink owns the attempt
	// header it writes, and what has to be modelled is a publisher that is not
	// us. WireMessage is the wire contract, so this stays honest to it.
	body, err := json.Marshal(mailoutbox.WireMessage{
		OutboxID: 1, Template: mailer.TemplateLoginCode, Recipient: "grace@x.test",
		Locale: "en", Ciphertext: ct, Nonce: nonce,
	})
	require.NoError(t, err)
	publishRaw(t, url, tp, body, amqp.Table{
		mailoutbox.AttemptHeader: int64(math.MaxInt64),
	})

	// One delivery, one refusal, straight to the dead queue: the counter arrives
	// already past any budget, so there is no rung left to try.
	require.Eventually(t, func() bool {
		return queueDepth(t, url, tp.Exchange+".dead") == 1
	}, 30*time.Second, 200*time.Millisecond)
	require.Equal(t, 0, queueDepth(t, url, tp.Exchange+".retry.1m"))
	require.Equal(t, 0, queueDepth(t, url, tp.Exchange+".retry.30m"))
}

// publishRaw puts a message on the send queue with headers of the test's
// choosing. The sink deliberately owns the header it writes, which is exactly
// what has to be bypassed to model a publisher that is not us.
func publishRaw(t *testing.T, url string, tp mailoutbox.Topology, body []byte, headers amqp.Table) {
	t.Helper()
	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	require.NoError(t, err)
	defer func() { _ = ch.Close() }()
	// Declared here because this publisher bypasses the sink, and the sink is
	// what normally brings the topology into being on its first delivery. Both
	// declarations are idempotent and identical, so this races harmlessly with
	// the worker's own — without it the publish lands on an exchange that does
	// not exist yet and is dropped, which reads as "the worker never routed it".
	require.NoError(t, tp.Declare(ch))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, ch.PublishWithContext(ctx, tp.Exchange, tp.RoutingKey, false, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Headers:      headers,
			Body:         body,
		}))
}

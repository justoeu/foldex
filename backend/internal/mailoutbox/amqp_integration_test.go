//go:build integration

package mailoutbox

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"foldex/internal/mailer"
)

// rabbitImage is pinned for the same reason every other test image is: a
// topology argument RabbitMQ accepts today and rejects tomorrow would turn into
// a PRECONDITION_FAILED that looks like our bug.
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
	t.Cleanup(func() {
		_ = testcontainers.TerminateContainer(c)
	})

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "5672/tcp")
	require.NoError(t, err)
	return "amqp://guest:guest@" + host + ":" + port.Port() + "/"
}

func testOutbox(t *testing.T) *Outbox {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	o, err := NewFromMasterKey(key)
	require.NoError(t, err)
	return o
}

// Declare is called by BOTH the relay and the worker on every connect, so it
// has to be idempotent against a topology that already exists. A second call
// that disagreed with the first would close the channel with PRECONDITION_FAILED
// and present as "the broker is down".
func TestAMQP_DeclareIsIdempotentAcrossConnections(t *testing.T) {
	url := startRabbit(t)
	tp := Topology{}.WithDefaults()

	for i := range 3 {
		conn, err := amqp.Dial(url)
		require.NoErrorf(t, err, "connection %d", i)
		ch, err := conn.Channel()
		require.NoError(t, err)
		require.NoErrorf(t, tp.Declare(ch), "declare %d", i)
		require.NoError(t, ch.Close())
		require.NoError(t, conn.Close())
	}
}

// The end-to-end property the transport exists for: what the relay publishes is
// what a consumer can open, and the broker never held it in the clear.
func TestAMQP_PublishedMessageArrivesSealedAndOpensToTheSameParams(t *testing.T) {
	url := startRabbit(t)
	o := testOutbox(t)
	tp := Topology{}.WithDefaults()

	sink, err := NewAMQPSink(AMQPConfig{URL: url, Topology: tp})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sink.Close() })

	link := "https://foldex.test/#reset=super-secret-token"
	sealed := sealForTest(t, o, map[string]string{"Link": link, "Name": "Grace"})
	sealed.ID = 7
	sealed.Template = mailer.TemplatePasswordReset
	sealed.Recipient = "grace@x.test"
	sealed.Locale = "pt"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, sink.Deliver(ctx, sealed))

	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NoError(t, tp.Declare(ch))

	d, ok, err := ch.Get(tp.QueueName(), true)
	require.NoError(t, err)
	require.True(t, ok, "the relay's publish should be sitting on the send queue")

	// The credential must not be readable from the wire.
	require.NotContains(t, string(d.Body), link)
	require.Equal(t, 0, Attempt(d.Headers), "a first delivery starts at attempt 0")

	msg, err := DecodeWire(d.Body)
	require.NoError(t, err)
	require.Equal(t, int64(7), msg.OutboxID)
	require.Equal(t, "pt", msg.Locale)

	env, err := o.OpenWire(msg)
	require.NoError(t, err)
	require.Equal(t, link, env.Params["Link"])
	require.Equal(t, "grace@x.test", env.To)
}

// Republish is the retry ladder. The message has to come BACK to the send queue
// once its step expires — if the binding or the dead-letter routing were wrong
// it would sit in the retry queue forever and nobody would notice, because
// nothing errors.
func TestAMQP_RepublishRidesTheLadderBackOntoTheSendQueue(t *testing.T) {
	url := startRabbit(t)
	tp := Topology{Exchange: "foldex.test.mail"}.WithDefaults()

	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	pub, err := NewConfirmingChannel(conn)
	require.NoError(t, err)
	ch := pub.Raw()
	require.NoError(t, tp.Declare(ch))

	body := []byte(`{"outbox_id":9,"template":"login_code","recipient":"g@x.test"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Attempt 1 lands on the 1-minute rung. Asserting it ARRIVED there is the
	// part that matters; waiting out the TTL would make this a one-minute test.
	require.NoError(t, tp.Republish(ctx, pub, body, 1, "send_failed", false))
	q, err := ch.QueueInspect(tp.Exchange + ".retry.1m")
	require.NoError(t, err)
	require.Equal(t, 1, q.Messages, "attempt 1 belongs on the first rung")

	// Giving up routes to the dead queue instead, carrying the reason the
	// backend settles the outbox row with — and never the payload.
	require.NoError(t, tp.Republish(ctx, pub, body, 4, "send_failed", true))
	dead, err := ch.QueueInspect(tp.deadQueue())
	require.NoError(t, err)
	require.Equal(t, 1, dead.Messages)

	d, ok, err := ch.Get(tp.deadQueue(), true)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 4, Attempt(d.Headers))
	require.Equal(t, "send_failed", d.Headers[ReasonHeader])
}

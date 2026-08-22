//go:build integration

package mailoutbox

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
)

// This is the seam the unit tests cannot reach: warnIfNobodyIsListening is
// correct in isolation whether or not anything calls it, and the consumer count
// is correct only if it comes from the declare rather than a constant. Deleting
// the call in channelLocked, or hardcoding the count, leaves every unit test in
// this package green — which is precisely how the missing worker went unnoticed
// on a live instance for a day.
func TestAMQPSink_WarnsWhenItPublishesIntoAQueueNobodyReads(t *testing.T) {
	url := startRabbit(t)
	o := testOutbox(t)
	tp := topologyFor(t)

	deliver := func(sink *AMQPSink) {
		t.Helper()
		msg := sealForTest(t, o, map[string]string{mailer.ParamCode: "123456", mailer.ParamExpiresMinutes: "10"})
		msg.ID = 1
		msg.Template = mailer.TemplateLoginCode
		msg.Recipient = "grace@x.test"
		msg.Locale = "en"
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, sink.Deliver(ctx, msg))
	}

	// No consumer anywhere: the publish succeeds and the message is stranded.
	var quiet bytes.Buffer
	lonely, err := NewAMQPSink(AMQPConfig{URL: url, Topology: tp,
		Logger: slog.New(slog.NewJSONHandler(&quiet, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	require.NoError(t, err)
	deliver(lonely)
	require.NoError(t, lonely.Close())
	require.Contains(t, quiet.String(), "no consumer",
		"a publish into an unread queue must not look like a delivery")
	require.Contains(t, quiet.String(), tp.QueueName())

	// The backlog has to come from the broker. Hardcoding Messages to 0 in
	// Declare survived every other test here, and the warning would then always
	// read `waiting: 0` on a queue holding hundreds of undelivered reset links
	// — a number an operator reasonably reads as "nothing is stuck yet".
	var backlog bytes.Buffer
	seeded, err := NewAMQPSink(AMQPConfig{URL: url, Topology: tp,
		Logger: slog.New(slog.NewJSONHandler(&backlog, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	require.NoError(t, err)
	deliver(seeded)
	deliver(seeded)
	require.NoError(t, seeded.Close())

	// A fresh connection re-declares, and by now three messages are waiting.
	var counted bytes.Buffer
	observer, err := NewAMQPSink(AMQPConfig{URL: url, Topology: tp,
		Logger: slog.New(slog.NewJSONHandler(&counted, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	require.NoError(t, err)
	deliver(observer)
	require.NoError(t, observer.Close())

	var rec map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(counted.Bytes()), &rec))
	require.EqualValues(t, 3, rec["waiting"], "the backlog must be read from the broker")

	// Attach a real consumer, then connect a fresh sink: the same code path now
	// sees someone listening and stays silent.
	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	require.NoError(t, err)
	_, declErr := tp.Declare(ch)
	require.NoError(t, declErr)
	_, err = ch.Consume(tp.QueueName(), "", true, false, false, false, nil)
	require.NoError(t, err)

	var busy bytes.Buffer
	attended, err := NewAMQPSink(AMQPConfig{URL: url, Topology: tp,
		Logger: slog.New(slog.NewJSONHandler(&busy, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	require.NoError(t, err)
	t.Cleanup(func() { _ = attended.Close() })
	deliver(attended)
	require.Empty(t, busy.String(), "a healthy stack must produce no warning at all")
}

package mailoutbox

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func captureSink(t *testing.T, tp Topology) (*AMQPSink, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	s, err := NewAMQPSink(AMQPConfig{
		URL:      "amqp://127.0.0.1:1/",
		Topology: tp,
		Logger:   slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	require.NoError(t, err)
	return s, &buf
}

// Publishing to a bound queue nobody reads succeeds, is confirmed, and settles
// the row as `published` — indistinguishable from a delivered message in every
// record the system keeps. This warning is the only place that difference is
// visible before a user reports the e-mail never arrived.
func TestWarnIfNobodyIsListening_ReportsAQueueWithNoConsumer(t *testing.T) {
	s, buf := captureSink(t, Topology{})
	s.warnIfNobodyIsListening(SendQueueState{Consumers: 0, Messages: 7})

	var rec map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec))
	require.Equal(t, "WARN", rec["level"])
	require.Equal(t, DefaultQueue, rec["queue"])
	require.EqualValues(t, 7, rec["waiting"], "the backlog is what makes the warning actionable")
	require.Contains(t, rec["msg"], "never sent")
}

// One consumer is enough. A warning that also fired on a healthy stack would be
// trained away within a week, and then the real one would be ignored too.
func TestWarnIfNobodyIsListening_SaysNothingWhenSomeoneIsConsuming(t *testing.T) {
	s, buf := captureSink(t, Topology{})
	s.warnIfNobodyIsListening(SendQueueState{Consumers: 1, Messages: 500})
	require.Empty(t, buf.String())
}

// The queue named must be the CONFIGURED one: a shared broker hosts more than
// one foldex, and a warning naming the default would send an operator to look
// at a queue their instance never touches.
func TestWarnIfNobodyIsListening_NamesTheConfiguredQueue(t *testing.T) {
	s, buf := captureSink(t, Topology{Queue: "tenant7.mail.send"})
	s.warnIfNobodyIsListening(SendQueueState{})
	require.Contains(t, buf.String(), "tenant7.mail.send")
}

// A nil logger must still WARN, not merely not panic.
//
// require.NotPanics alone was the original assertion and it could not tell a
// working fallback from a silent give-up: replacing the slog.Default() line
// with a bare `return` kept it green. That matters because the one line wiring
// a logger in main.go is a plausible merge casualty, and losing it would turn
// this feature off completely — reintroducing, at the constructor, exactly the
// failure it exists to catch.
func TestWarnIfNobodyIsListening_StillWarnsThroughTheDefaultLogger(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	s, err := NewAMQPSink(AMQPConfig{URL: "amqp://127.0.0.1:1/"})
	require.NoError(t, err)
	require.NotPanics(t, func() { s.warnIfNobodyIsListening(SendQueueState{}) })
	require.Contains(t, buf.String(), "no consumer")
}

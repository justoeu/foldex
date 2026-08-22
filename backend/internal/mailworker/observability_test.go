package mailworker

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
	"foldex/internal/mailoutbox"
)

// capture returns a logger writing JSON records into buf, so a test can assert
// on what an operator would actually read.
func capture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})), &buf
}

func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal(line, &rec))
		out = append(out, rec)
	}
	return out
}

func wireBody(t *testing.T, msg mailoutbox.WireMessage) []byte {
	t.Helper()
	body, err := json.Marshal(msg)
	require.NoError(t, err)
	return body
}

// A worker that only ever logs its own startup cannot answer "did it send
// anything?", which is the first question asked when a user reports a missing
// e-mail. This is the record that answers it.
func TestHandle_LogsAnAffirmativeRecordOfEverySend(t *testing.T) {
	o, _ := testOutbox(t)
	logger, buf := capture()
	w := newTestWorker(t, o, &fakeMailer{})
	w.logger = logger

	msg := seal(t, o, mailer.TemplateLoginCode, "pt", loginCodeParams())
	msg.OutboxID = 4242
	w.handle(nil, amqp.Delivery{Body: wireBody(t, msg)})

	recs := records(t, buf)
	require.Len(t, recs, 1, "a successful send must leave exactly one record")
	require.Equal(t, "mail sent", recs[0]["msg"])
	require.Equal(t, "INFO", recs[0]["level"])
	require.EqualValues(t, 4242, recs[0]["outbox_id"])
	require.Equal(t, mailer.TemplateLoginCode, recs[0]["template"])
}

// The record identifies the message, never the person. logsafe redacts the key
// `email`; it does not redact `recipient`, so an address logged under that name
// would sit in plain text in every deployment's log — and this is the one
// process that handles a live reset link.
func TestHandle_SendRecordNeverCarriesTheRecipientAddress(t *testing.T) {
	o, _ := testOutbox(t)
	logger, buf := capture()
	w := newTestWorker(t, o, &fakeMailer{})
	w.logger = logger

	msg := seal(t, o, mailer.TemplateLoginCode, "pt", loginCodeParams())
	require.Equal(t, "grace@x.test", msg.Recipient, "fixture must carry an address to leak")
	w.handle(nil, amqp.Delivery{Body: wireBody(t, msg)})

	require.NotContains(t, buf.String(), "grace@x.test")
	require.NotContains(t, buf.String(), "recipient")
}

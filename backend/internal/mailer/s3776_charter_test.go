package mailer

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3776Charter_Send(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("log driver succeeds", func(t *testing.T) {
		t.Parallel()
		m, err := New(Config{Driver: "log"}, logger)
		require.NoError(t, err)
		require.NoError(t, m.Send(context.Background(), Message{To: "a@b.c", Subject: "s", Text: "hello"}))
	})

	t.Run("smtp dial failure", func(t *testing.T) {
		t.Parallel()
		m, err := New(Config{
			Driver:  "smtp",
			Host:    "127.0.0.1",
			Port:    1,
			From:    "foldex@localhost",
			Timeout: 200 * time.Millisecond,
		}, logger)
		require.NoError(t, err)
		err = m.Send(context.Background(), Message{To: "a@b.c", Subject: "s", Text: "hello"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dial")
	})
}

func TestS3776Charter_LoadAssets(t *testing.T) {
	t.Parallel()
	good := fstest.MapFS{
		"templates/x.html.tmpl":     {Data: []byte(`{{ define "x" }}<p>{{ .Heading }}</p>{{ end }}`)},
		"templates/layout.txt.tmpl": {Data: []byte(`{{ define "text.x" }}{{ .Heading }}{{ end }}`)},
		"templates/strings.en.json": {Data: []byte(`{"x":{"subject":"s","heading":"h"}}`)},
	}
	_, err := loadAssets(good)
	require.NoError(t, err)

	broken := fstest.MapFS{
		"templates/x.html.tmpl":     {Data: []byte(`{{ define "x" }}{{ .Heading`)},
		"templates/layout.txt.tmpl": {Data: []byte(`{{ define "text.x" }}h{{ end }}`)},
		"templates/strings.en.json": {Data: []byte(`{"x":{"subject":"s","heading":"h"}}`)},
	}
	_, err = loadAssets(broken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse html layouts")
}

func TestS3776Charter_AssetsRender(t *testing.T) {
	t.Parallel()
	msg, err := std.render(InviteMessage("a@b.c", "Ada", "https://x.test/accept", 48), "en")
	require.NoError(t, err)
	assert.Equal(t, "a@b.c", msg.To)
	assert.NotEmpty(t, msg.Subject)
	assert.NotEmpty(t, msg.Text)
	assert.NotEmpty(t, msg.HTML)

	_, err = std.render(Envelope{Template: "does-not-exist", To: "a@b.c"}, "en")
	require.ErrorIs(t, err, ErrUnknownTemplate)
}

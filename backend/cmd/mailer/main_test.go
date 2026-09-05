package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withMailerBootEnv is the minimum for LoadMailer to succeed AND for the
// driver check to be the thing under test. A missing AUTH_ENCRYPTION_KEY is
// deliberate: if the worker only WARNs on MAIL_DRIVER=log and continues, the
// next line is the key load, and that is how the test sees the leak-path is
// still open.
func withMailerBootEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MAIL_TRANSPORT", "AMQP_URL", "AMQP_ALLOW_PLAINTEXT",
		"MAIL_DRIVER", "MAIL_HOST", "MAIL_FROM",
		"AUTH_ENCRYPTION_KEY",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_URL", "amqps://broker.example:5671/foldex")
	// envOr falls back to /data/auth_encryption.key when the var is empty,
	// and a leftover file there would skip the "not configured" error the
	// smtp-accepted case needs to observe.
	t.Setenv("AUTH_ENCRYPTION_KEY_PATH", filepath.Join(t.TempDir(), "missing.key"))
}

func TestRun_NonSMTPDriverRefusesToBoot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		driver string
	}{
		{name: "log is the documented default and the leak", driver: "log"},
		{name: "unset defaults to log", driver: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withMailerBootEnv(t)
			if tc.driver != "" {
				t.Setenv("MAIL_DRIVER", tc.driver)
			}

			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			code := run(logger)
			logs := buf.String()

			require.Equal(t, 1, code)
			assert.Contains(t, logs, "mailer requires MAIL_DRIVER=smtp",
				"AMQP + log is a misconfig: the worker has just decrypted live reset links")
			assert.NotContains(t, logs, "auth encryption key",
				"must fail closed before loading the key that opens the payloads")
			assert.NotContains(t, logs, "message bodies will be logged")
		})
	}
}

func TestRun_SMTPDriverIsNotRefusedAtTheDriverGate(t *testing.T) {
	withMailerBootEnv(t)
	t.Setenv("MAIL_DRIVER", "smtp")
	t.Setenv("MAIL_HOST", "mail.example")
	t.Setenv("MAIL_FROM", "foldex@localhost")

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	code := run(logger)
	logs := buf.String()

	require.Equal(t, 1, code)
	assert.NotContains(t, logs, "mailer requires MAIL_DRIVER=smtp")
	assert.Contains(t, logs, "auth encryption key",
		"smtp is the intended driver; boot should proceed to the next dependency")
}

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// withCleanEnv satisfies the one hard requirement of Load and CLEARS everything
// else Load validates, so each case below is about the variable it sets and
// nothing else.
//
// Clearing is not defensive tidiness. backend/Makefile does "include ../.env"
// plus "export", so "make coverage-run" hands the whole local .env to the test
// binary, while CI has no .env and skips the include entirely. A case that only
// sets DB_URL therefore asserts against a DIFFERENT configuration on a laptop
// than on CI, and the divergence surfaces as a red suite for whoever is
// mid-change, pointing nowhere near the cause. An operator running an AMQP
// broker is exactly who these cases are for, and exactly whose .env breaks them.
//
// The list is what Load's validators read: validateMailTransport
// (MAIL_TRANSPORT, AMQP_URL), validateSecureDefaults (BACKEND_BIND,
// AUTH_ENABLED, MAIL_INSECURE_SKIP_VERIFY, MAIL_HOST) and normalizeAuth
// (AUTH_PUBLIC_URL). Empty reads as unset to every one of them.
func withCleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MAIL_TRANSPORT", "AMQP_URL",
		"BACKEND_BIND", "AUTH_ENABLED", "AUTH_PUBLIC_URL",
		"MAIL_INSECURE_SKIP_VERIFY", "MAIL_HOST",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("DB_URL", "postgres://x@y/z")
}

func TestLoad_MailTransportDefaultsToInproc(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, MailTransportInproc, cfg.Mail.Transport)
	require.False(t, cfg.Mail.UsesBroker())
}

// An unknown transport must REFUSE, not fall back. Falling back is the worst
// outcome available: mail keeps working, so nothing looks broken, while every
// message the operator expects on the broker is quietly sent by the backend.
func TestLoad_UnknownMailTransportRefusesToBoot(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "rabbit")

	_, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "MAIL_TRANSPORT")
}

func TestLoad_AMQPTransportRequiresAURL(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_URL", "")

	_, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "AMQP_URL")

	// Whitespace is not a URL either — an operator who left `AMQP_URL= ` in a
	// .env would otherwise get a dial failure loop instead of a clear refusal.
	t.Setenv("AMQP_URL", "   ")
	_, err = Load()
	require.Error(t, err)
}

// Plaintext AMQP to a remote broker puts the broker password on the network in
// clear, which is the same failure MAIL_INSECURE_SKIP_VERIFY is refused for.
func TestLoad_PlaintextAMQPToARemoteBrokerIsRefused(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_URL", "amqp://foldex:secret@broker.example:5672/foldex")

	_, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "amqps://")
	// The refusal names the host so it is actionable, and never the credential.
	require.NotContains(t, err.Error(), "secret")
}

func TestLoad_AMQPIsAcceptedOverTLSOrAgainstLoopback(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")

	// TLS to anywhere is fine: the credential is protected in transit.
	t.Setenv("AMQP_URL", "amqps://foldex:secret@broker.example:5671/foldex")
	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.Mail.UsesBroker())

	// Plaintext to loopback is fine too — a broker on the same host, which is
	// the ordinary compose deployment.
	t.Setenv("AMQP_URL", "amqp://foldex:secret@localhost:5672/foldex")
	_, err = Load()
	require.NoError(t, err)
}

func TestLoad_MailThroughputKnobsAreClampedNotRejected(t *testing.T) {
	withCleanEnv(t)

	t.Setenv("AMQP_PREFETCH", "-5")
	t.Setenv("MAIL_OUTBOX_BATCH", "0")
	t.Setenv("MAIL_OUTBOX_POLL_SEC", "0")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 4, cfg.Mail.AMQPPrefetch)
	require.Equal(t, 32, cfg.Mail.OutboxBatch)
	require.Equal(t, 5, cfg.Mail.OutboxPollSec)

	t.Setenv("AMQP_PREFETCH", "9000")
	t.Setenv("MAIL_OUTBOX_BATCH", "100000")
	t.Setenv("MAIL_OUTBOX_POLL_SEC", "100000")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, 64, cfg.Mail.AMQPPrefetch)
	require.Equal(t, 512, cfg.Mail.OutboxBatch)
	require.Equal(t, 300, cfg.Mail.OutboxPollSec)
}

// The worker must boot with NO database credential. That is the whole reason
// it is a separate binary: it is the process that decrypts live reset links,
// and requiring a DSN it never opens would undo the isolation.
func TestLoadMailer_NeedsNoDatabaseURL(t *testing.T) {
	t.Setenv("DB_URL", "")
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_URL", "amqps://broker.example:5671/foldex")

	cfg, err := LoadMailer()
	require.NoError(t, err)
	require.True(t, cfg.Mail.UsesBroker())
	require.Empty(t, cfg.DBURL)

	// It still applies the transport refusals, so a misconfigured worker fails
	// at boot rather than consuming a queue nothing publishes to.
	t.Setenv("AMQP_URL", "")
	_, err = LoadMailer()
	require.Error(t, err)
}

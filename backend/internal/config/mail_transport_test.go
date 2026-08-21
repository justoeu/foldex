package config

import (
	"strings"
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
		"MAIL_TRANSPORT", "AMQP_URL", "AMQP_ALLOW_PLAINTEXT",
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

// The opt-in exists because "remote broker" and "broker on my LAN" are not the
// same risk, and the original guard could not tell them apart.
func TestLoad_PlaintextAMQPIsRefusedByDefault(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_URL", "amqp://u:p@192.168.68.70:5672/foldex")

	_, err := Load()
	require.Error(t, err)
	// The message has to NAME the escape, or the operator's only path from here
	// is reading the source.
	require.Contains(t, err.Error(), "AMQP_ALLOW_PLAINTEXT")
}

func TestLoad_PlaintextAMQPIsAllowedToAPrivateAddress(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_URL", "amqp://u:p@192.168.68.70:5672/foldex")
	t.Setenv("AMQP_ALLOW_PLAINTEXT", "1")

	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.Mail.UsesBroker())
}

// The flag is a claim about the NETWORK, so it is verified against the network.
// Without this the realistic typo — one wrong octet — publishes the broker
// password to a stranger on every message, and nothing reports it.
func TestLoad_PlaintextAMQPIsStillRefusedForAPublicAddress(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_ALLOW_PLAINTEXT", "1")

	for _, host := range []string{"203.0.113.4", "8.8.8.8"} {
		t.Setenv("AMQP_URL", "amqp://u:p@"+host+":5672/foldex")
		_, err := Load()
		require.Error(t, err, "public %s must stay refused even with the opt-in", host)
		require.Contains(t, err.Error(), "PUBLIC")
	}
}

// Every private range the deployment might use, not just the one the author
// happened to have. A guard that covered 192.168/16 alone would refuse a
// perfectly ordinary 10.x or 172.16.x broker and read as a bug.
func TestLoad_PlaintextAMQPCoversEveryPrivateRange(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_ALLOW_PLAINTEXT", "1")

	allowed := []string{
		"10.1.2.3", "172.16.5.6", "192.168.1.10",
		// CGNAT edges, not just the interior. A test that only exercised
		// 100.64.0.1 leaves the bounds free to drift: widening the upper one to
		// <=128 would silently swallow 100.128.0.0/9, which is ALLOCATED PUBLIC
		// space — plaintext to a stranger, permitted by an off-by-one nobody
		// could see fail.
		"100.64.0.0", "100.127.255.255",
		// Loopback beyond the four literals isLocalBind knows. 127.0.0.2 is not
		// RFC1918, so it reaches here and is allowed only because
		// isPrivateNetworkIP consults IsLoopback.
		"127.0.0.2",
		// IPv6: ULA and an IPv4-mapped private address.
		"fd00::1", "::ffff:192.168.1.1",
		// Link-local. Implausible as a broker address, but by definition it cannot
		// leave the local link, so permitting it is coherent -- and permission that
		// no test exercises is permission nobody can see change.
		"169.254.1.5", "fe80::1",
	}
	for _, host := range allowed {
		t.Setenv("AMQP_URL", "amqp://u:p@"+bracket(host)+":5672/foldex")
		_, err := Load()
		require.NoError(t, err, "private %s must be allowed under the opt-in", host)
	}

	// The other side of each boundary. Without these the range checks are only
	// half-specified, and a widened bound reads as passing.
	for _, host := range []string{"100.63.255.255", "100.128.0.0", "2001:db8::1", "::ffff:8.8.8.8"} {
		t.Setenv("AMQP_URL", "amqp://u:p@"+bracket(host)+":5672/foldex")
		_, err := Load()
		require.Error(t, err, "%s is outside every private range and must stay refused", host)
	}
}

// bracket wraps an IPv6 literal for use in a URL authority. url.URL.Hostname()
// strips the brackets again, which is why the guard sees a parseable address
// rather than falling through to the hostname branch.
func bracket(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// A name cannot be resolved without putting DNS on the boot path, and the answer
// could change afterwards regardless — so the flag is taken at its word here.
// Locked so the behaviour is a decision on record rather than an oversight.
func TestLoad_PlaintextAMQPAcceptsAHostnameOnTheOperatorsWord(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_ALLOW_PLAINTEXT", "1")
	t.Setenv("AMQP_URL", "amqp://u:p@rabbitmq.internal:5672/foldex")

	_, err := Load()
	require.NoError(t, err)
}

// amqps:// never needed the flag and must not start needing it.
func TestLoad_TLSBrokerIsUnaffectedByThePlaintextFlag(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_URL", "amqps://u:p@203.0.113.4:5671/foldex")

	_, err := Load()
	require.NoError(t, err, "a public TLS broker is the normal case and needs no opt-in")
}

// A posture relaxed by configuration has to leave a trace, or the person reading
// the logs during an incident has no way to learn the broker credential is on
// the wire in clear. Same reasoning as the TRUSTED_PROXY_IPS warning in
// internal/server/router.go, which is likewise locked by a test.
func TestPlaintextBrokerWarning_FiresOnlyWhenTheRelaxationIsInForce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		wantMsg bool
	}{
		{"flag off, plaintext refused anyway", map[string]string{
			"MAIL_TRANSPORT": "amqp", "AMQP_URL": "amqp://u:p@127.0.0.1:5672/"}, false},
		{"flag on but broker is loopback", map[string]string{
			"MAIL_TRANSPORT": "amqp", "AMQP_URL": "amqp://u:p@127.0.0.1:5672/",
			"AMQP_ALLOW_PLAINTEXT": "1"}, false},
		{"flag on and broker is remote — the case that matters", map[string]string{
			"MAIL_TRANSPORT": "amqp", "AMQP_URL": "amqp://u:p@192.168.68.70:5672/foldex",
			"AMQP_ALLOW_PLAINTEXT": "1"}, true},
		{"amqps never warns, flag or not", map[string]string{
			"MAIL_TRANSPORT": "amqp", "AMQP_URL": "amqps://u:p@192.168.68.70:5671/foldex",
			"AMQP_ALLOW_PLAINTEXT": "1"}, false},
		{"inproc never warns", map[string]string{
			"MAIL_TRANSPORT": "inproc", "AMQP_ALLOW_PLAINTEXT": "1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCleanEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg, err := Load()
			require.NoError(t, err)

			got := cfg.PlaintextBrokerWarning()
			if !tc.wantMsg {
				require.Empty(t, got)
				return
			}
			require.NotEmpty(t, got)
			// Name the flag and the destination: a warning that says only
			// "insecure" leaves the reader to guess which knob and which host.
			require.Contains(t, got, "AMQP_ALLOW_PLAINTEXT")
			require.Contains(t, got, "192.168.68.70")
			// And it must NOT overstate: the payload stays sealed.
			require.Contains(t, got, "payload")
		})
	}
}

// The warning must never carry the credential it is warning about.
func TestPlaintextBrokerWarning_NeverEchoesTheCredential(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("MAIL_TRANSPORT", "amqp")
	t.Setenv("AMQP_URL", "amqp://broker-user:hunter2@192.168.68.70:5672/foldex")
	t.Setenv("AMQP_ALLOW_PLAINTEXT", "1")

	cfg, err := Load()
	require.NoError(t, err)

	got := cfg.PlaintextBrokerWarning()
	require.NotEmpty(t, got)
	require.NotContains(t, got, "hunter2")
	require.NotContains(t, got, "broker-user")
}

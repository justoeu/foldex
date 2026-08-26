package backupagent

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setBaseline gives Load the minimum it accepts, so each test flips exactly
// the knob under scrutiny.
func setBaseline(t *testing.T) {
	t.Helper()
	t.Setenv("BACKUP_S3_ENDPOINT", "s3.example.com")
	t.Setenv("BACKUP_S3_BUCKET", "foldex-backups")
	t.Setenv("BACKUP_S3_ACCESS_KEY", "ak")
	t.Setenv("BACKUP_S3_SECRET_KEY", "sk")
	t.Setenv("BACKUP_AGE_RECIPIENTS", "age1qqnl0eg9annqfy0596hyp2pkjsjm0cvqp23exd6yzvfhq7fyf5dsegvzt5")
	t.Setenv("BACKUP_ALLOW_PLAINTEXT", "")
	t.Setenv("BACKUP_DUMP_AT", "")
	t.Setenv("BACKUP_DRILL_AT", "")
	t.Setenv("BACKUP_AGE_IDENTITY_FILE", "")
	t.Setenv("BACKUP_RETENTION_MODE", "")
	t.Setenv("POSTGRES_PASSWORD", "pw")
	// The mirror defaults ON (360 min) and then requires the source secret.
	t.Setenv("RUSTFS_SECRET_KEY", "rustfs-sk")
	// A developer's stray environment must not leak into the assertions.
	for _, k := range []string{"BACKUP_RETAIN_DAILY", "BACKUP_RETAIN_WEEKLY", "BACKUP_RETAIN_MONTHLY",
		"BACKUP_METRICS_ADDR", "BACKUP_S3_REGION", "BACKUP_S3_USE_SSL",
		"BACKUP_REQUESTED_POLL_SEC", "BACKUP_STALE_RUN_MIN", "BACKUP_MIRROR_INTERVAL_MIN",
		"RUSTFS_ENDPOINT", "RUSTFS_ACCESS_KEY", "RUSTFS_BUCKET", "RUSTFS_USE_SSL"} {
		t.Setenv(k, "")
	}
}

func TestLoad_RefusesToBootHalfConfigured(t *testing.T) {
	for _, missing := range []string{"BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_ACCESS_KEY", "BACKUP_S3_SECRET_KEY"} {
		t.Run(missing, func(t *testing.T) {
			setBaseline(t)
			t.Setenv(missing, "")
			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), missing, "the boot error must NAME the missing var")
		})
	}
}

func TestLoad_PlaintextIsAnExplicitOptOutNeverAFallback(t *testing.T) {
	setBaseline(t)
	t.Setenv("BACKUP_AGE_RECIPIENTS", "")
	_, err := Load()
	require.Error(t, err, "no recipients and no opt-out must refuse to boot")
	assert.Contains(t, err.Error(), "BACKUP_ALLOW_PLAINTEXT")

	t.Setenv("BACKUP_ALLOW_PLAINTEXT", "1")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.AgeRecipients)
	assert.True(t, cfg.AllowPlaintext)
}

func TestLoad_RetentionModeIsDeclaredNeverInferred(t *testing.T) {
	setBaseline(t)
	t.Setenv("BACKUP_RETENTION_MODE", "auto")
	_, err := Load()
	require.Error(t, err)

	for _, mode := range []string{"agent", "bucket"} {
		t.Setenv("BACKUP_RETENTION_MODE", mode)
		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, mode, cfg.RetentionMode)
	}
}

func TestLoad_AnchorAndRecipientsParse(t *testing.T) {
	setBaseline(t)
	t.Setenv("BACKUP_DUMP_AT", "03:30")
	t.Setenv("BACKUP_AGE_RECIPIENTS",
		"age1qqnl0eg9annqfy0596hyp2pkjsjm0cvqp23exd6yzvfhq7fyf5dsegvzt5 , age1qqnl0eg9annqfy0596hyp2pkjsjm0cvqp23exd6yzvfhq7fyf5dsegvzt5")
	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.DumpAt.Enabled())
	assert.Len(t, cfg.AgeRecipients, 2, "comma-separated with stray spaces is how humans type lists")

	t.Setenv("BACKUP_DUMP_AT", "quarter past three")
	_, err = Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BACKUP_DUMP_AT")
}

func TestLoad_DrillScheduleAndIdentity(t *testing.T) {
	t.Run("anchor parses with its weekday", func(t *testing.T) {
		setBaseline(t)
		t.Setenv("BACKUP_DRILL_AT", "04:30 sun")
		t.Setenv("BACKUP_AGE_IDENTITY_FILE", "/run/secrets/backup-age-identity")
		cfg, err := Load()
		require.NoError(t, err)
		assert.True(t, cfg.DrillAt.Enabled())
		assert.True(t, cfg.DrillAt.Weekly)
		assert.Equal(t, "/run/secrets/backup-age-identity", cfg.AgeIdentityFile)
	})

	t.Run("a bad anchor names its var", func(t *testing.T) {
		setBaseline(t)
		t.Setenv("BACKUP_DRILL_AT", "sunday morning")
		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BACKUP_DRILL_AT")
	})

	t.Run("scheduled drill over encrypted dumps demands the identity", func(t *testing.T) {
		setBaseline(t)
		t.Setenv("BACKUP_DRILL_AT", "04:30 sun")
		_, err := Load()
		require.Error(t, err, "booting fine and failing every week at 04:30 is the silent non-backup shape")
		assert.Contains(t, err.Error(), "BACKUP_AGE_IDENTITY_FILE")
	})

	t.Run("plaintext deployments drill without an identity", func(t *testing.T) {
		setBaseline(t)
		t.Setenv("BACKUP_AGE_RECIPIENTS", "")
		t.Setenv("BACKUP_ALLOW_PLAINTEXT", "1")
		t.Setenv("BACKUP_DRILL_AT", "04:30 sun")
		cfg, err := Load()
		require.NoError(t, err)
		assert.True(t, cfg.DrillAt.Enabled())
	})

	t.Run("default is off", func(t *testing.T) {
		setBaseline(t)
		cfg, err := Load()
		require.NoError(t, err)
		assert.False(t, cfg.DrillAt.Enabled(), "no anchor means no scheduled drill — opt-in, because it demands the private identity on the host")
	})
}

func TestDBURL_EscapesCredentials(t *testing.T) {
	// The password deliberately includes the characters that TELL the two
	// escaping schemes apart: QueryEscape turns a space into "+" (which
	// userinfo does NOT decode back) and would leave this test green while
	// silently breaking auth — the space, "%" and "+" are the revert
	// detectors, not decoration.
	cfg := Config{
		PGHost: "db", PGPort: 5432, PGDatabase: "foldex", PGSSLMode: "disable",
		PGUser: "user@corp", PGPassword: "p w+%end@x:/",
	}
	dsn := cfg.DBURL()
	assert.Contains(t, dsn, "user%40corp")
	assert.Contains(t, dsn, "p%20w+%25end%40x%3A%2F",
		"userinfo escaping: space is %20 (never \"+\"), %% is %25, literal + stays")

	// And the DSN must round-trip: what pgx parses back out is the password
	// the operator typed.
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	pw, ok := parsed.User.Password()
	require.True(t, ok)
	assert.Equal(t, "p w+%end@x:/", pw)
	assert.Equal(t, "user@corp", parsed.User.Username())
	assert.False(t, strings.Contains(dsn, "p w+"))
}

func TestLoad_Defaults(t *testing.T) {
	setBaseline(t)
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.RetainDaily)
	assert.Equal(t, 4, cfg.RetainWeekly)
	assert.Equal(t, 6, cfg.RetainMonthly)
	assert.Equal(t, "agent", cfg.RetentionMode)
	assert.Equal(t, ":9099", cfg.MetricsAddr)
	assert.Equal(t, "us-east-1", cfg.S3Region)
	assert.True(t, cfg.S3UseSSL)
	assert.False(t, cfg.DumpAt.Enabled(), "no anchor means the job stays off")
}

func TestDurationHelpers_CarryTheirUnits(t *testing.T) {
	cfg := Config{RequestedPollSec: 30, StaleRunMin: 240, MirrorIntervalMin: 360}
	// The unit lives in the env var NAME (repo convention); getting it wrong
	// here is a 1000x error — a 30ms poll hammers the database, a 4-second
	// stale TTL makes the janitor kill live runs.
	assert.Equal(t, 30*time.Second, cfg.RequestedPoll())
	assert.Equal(t, 240*time.Minute, cfg.StaleRunTTL())
	assert.Equal(t, 6*time.Hour, cfg.MirrorInterval())
}

func TestLoad_MirrorDefaultsOnAndDemandsItsSource(t *testing.T) {
	setBaseline(t)
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 360, cfg.MirrorIntervalMin, "SDD §4: mirror cadence defaults to 6h")
	assert.True(t, cfg.MirrorEnabled())
	assert.Equal(t, "rustfs:9000", cfg.RustFSEndpoint)
	assert.Equal(t, "foldex-screenshots", cfg.RustFSBucket)
	assert.False(t, cfg.RustFSUseSSL)

	// Mirror on + no source secret = refuse to boot NAMING the var and the
	// off switch: a mirror that silently never copies is the mailer incident.
	t.Setenv("RUSTFS_SECRET_KEY", "")
	_, err = Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RUSTFS_SECRET_KEY")
	assert.Contains(t, err.Error(), "BACKUP_MIRROR_INTERVAL_MIN=0")

	// Explicitly off: the source becomes irrelevant and boot proceeds.
	t.Setenv("BACKUP_MIRROR_INTERVAL_MIN", "0")
	cfg, err = Load()
	require.NoError(t, err)
	assert.False(t, cfg.MirrorEnabled())
}

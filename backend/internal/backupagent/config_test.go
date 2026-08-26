package backupagent

import (
	"strings"
	"testing"

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
	t.Setenv("BACKUP_RETENTION_MODE", "")
	t.Setenv("POSTGRES_PASSWORD", "pw")
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

func TestDBURL_EscapesCredentials(t *testing.T) {
	cfg := Config{
		PGHost: "db", PGPort: 5432, PGDatabase: "foldex", PGSSLMode: "disable",
		PGUser: "user@corp", PGPassword: "p@ss:w/rd",
	}
	url := cfg.DBURL()
	assert.Contains(t, url, "user%40corp")
	assert.Contains(t, url, "p%40ss%3Aw%2Frd",
		"an unescaped password with @ or : silently connects to the wrong host instead of failing")
	assert.False(t, strings.Contains(url, "p@ss:w/rd"))
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

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateSecureDefaults locks the P5.3 boot guard. The three knobs
// (bind, secret, cors) only trigger refusal in the exact combination
// "non-loopback bind + empty secret + wildcard CORS" — any single knob
// flipped to a safe value clears the check.
func TestValidateSecureDefaults(t *testing.T) {
	cases := []struct {
		name    string
		bind    string
		secret  string
		cors    []string
		wantErr bool
	}{
		{"localhost loopback default", "127.0.0.1", "", []string{"*"}, false},
		{"loopback alias", "localhost", "", []string{"*"}, false},
		{"ipv6 loopback", "::1", "", []string{"*"}, false},
		{"empty bind is loopback", "", "", []string{"*"}, false},
		// The dangerous combo:
		{"public bind + no secret + wildcard CORS", "0.0.0.0", "", []string{"*"}, true},
		{"public bind + LAN IP + no secret + wildcard CORS", "192.168.1.10", "", []string{"*"}, true},
		// Public bind but one knob constrained:
		{"public bind + secret set", "0.0.0.0", "topsecret", []string{"*"}, false},
		{"public bind + restricted CORS", "0.0.0.0", "", []string{"https://example"}, false},
		{"public bind + multi-origin without wildcard", "0.0.0.0", "", []string{"https://a", "https://b"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{BindAddr: tc.bind, SharedSecret: tc.secret, CORSOrigins: tc.cors}
			err := c.validateSecureDefaults()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "insecure config")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestLoad_RequiresDBURL(t *testing.T) {
	t.Setenv("DB_URL", "")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_URL")
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("BACKEND_PORT", "")
	t.Setenv("PREVIEW_WORKER_CONCURRENCY", "")
	t.Setenv("PREVIEW_FETCH_TIMEOUT_SEC", "")
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("SHARED_SECRET", "")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "9089", cfg.Port)
	assert.Equal(t, 4, cfg.PreviewConcurrency)
	assert.Equal(t, 5, cfg.PreviewTimeoutSec)
	assert.Equal(t, []string{"*"}, cfg.CORSOrigins)
	assert.Empty(t, cfg.SharedSecret)
}

func TestLoad_Overrides(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("BACKEND_PORT", "9090")
	t.Setenv("PREVIEW_WORKER_CONCURRENCY", "8")
	t.Setenv("PREVIEW_FETCH_TIMEOUT_SEC", "10")
	t.Setenv("CORS_ORIGINS", "http://localhost:9088, https://foldex.example")
	t.Setenv("SHARED_SECRET", "abc123")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "9090", cfg.Port)
	assert.Equal(t, 8, cfg.PreviewConcurrency)
	assert.Equal(t, 10, cfg.PreviewTimeoutSec)
	assert.Equal(t, []string{"http://localhost:9088", "https://foldex.example"}, cfg.CORSOrigins)
	assert.Equal(t, "abc123", cfg.SharedSecret)
}

func TestLoad_ClampsConcurrency(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("PREVIEW_WORKER_CONCURRENCY", "-3")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.PreviewConcurrency, "negative concurrency should be clamped to 1")
}

func TestLoad_IgnoresBadInts(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("PREVIEW_WORKER_CONCURRENCY", "not-a-number")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 4, cfg.PreviewConcurrency, "unparseable int should fall back to default")
}

func TestSplitCSV_TrimsAndDropsEmpty(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, splitCSV("a, b,  c"))
	assert.Equal(t, []string{"only"}, splitCSV("only"))
	assert.Empty(t, splitCSV(",,, ,"))
}

func TestLoad_MinIODefaults(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("MINIO_ENDPOINT", "")
	t.Setenv("MINIO_ACCESS_KEY", "")
	t.Setenv("MINIO_SECRET_KEY", "")
	t.Setenv("MINIO_BUCKET", "")
	t.Setenv("MINIO_USE_SSL", "")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "localhost:9000", cfg.MinIO.Endpoint)
	assert.Equal(t, "minioadmin", cfg.MinIO.AccessKey)
	assert.Equal(t, "minioadmin", cfg.MinIO.SecretKey)
	assert.Equal(t, "foldex-screenshots", cfg.MinIO.Bucket)
	assert.False(t, cfg.MinIO.UseSSL)
}

func TestLoad_MinIOOverrides(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("MINIO_ENDPOINT", "minio:9000")
	t.Setenv("MINIO_ACCESS_KEY", "mykey")
	t.Setenv("MINIO_SECRET_KEY", "mysecret")
	t.Setenv("MINIO_BUCKET", "mybucket")
	t.Setenv("MINIO_USE_SSL", "true")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "minio:9000", cfg.MinIO.Endpoint)
	assert.Equal(t, "mykey", cfg.MinIO.AccessKey)
	assert.Equal(t, "mysecret", cfg.MinIO.SecretKey)
	assert.Equal(t, "mybucket", cfg.MinIO.Bucket)
	assert.True(t, cfg.MinIO.UseSSL)
}

func TestEnvBool(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"false", false},
		{"0", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Setenv("TEST_BOOL", tc.val)
		assert.Equal(t, tc.want, envBool("TEST_BOOL", false), "value: %q", tc.val)
	}
}

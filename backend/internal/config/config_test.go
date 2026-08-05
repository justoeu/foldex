package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateSecureDefaults: non-loopback bind requires SHARED_SECRET.
// CORS is irrelevant to the auth decision.
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
		{"public bind + no secret + wildcard CORS", "0.0.0.0", "", []string{"*"}, true},
		{"public bind + LAN IP + no secret", "192.168.1.10", "", []string{"*"}, true},
		// Restricted CORS must NOT exempt missing secret on public bind:
		{"public bind + restricted CORS still needs secret", "0.0.0.0", "", []string{"https://example"}, true},
		{"public bind + multi-origin without secret", "0.0.0.0", "", []string{"https://a", "https://b"}, true},
		{"public bind + secret set", "0.0.0.0", "topsecret", []string{"*"}, false},
		{"public bind + secret + restricted CORS", "0.0.0.0", "topsecret", []string{"https://example"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{BindAddr: tc.bind, SharedSecret: tc.secret, CORSOrigins: tc.cors}
			err := c.validateSecureDefaults()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "insecure config")
				assert.Contains(t, err.Error(), "SHARED_SECRET")
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

func TestLoad_ObjectStoreDefaults(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	for _, k := range []string{
		"RUSTFS_ENDPOINT", "RUSTFS_ACCESS_KEY", "RUSTFS_SECRET_KEY", "RUSTFS_BUCKET", "RUSTFS_USE_SSL",
		"MINIO_ENDPOINT", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY", "MINIO_BUCKET", "MINIO_USE_SSL",
	} {
		t.Setenv(k, "")
	}

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "localhost:9000", cfg.ObjectStore.Endpoint)
	assert.Equal(t, "foldex", cfg.ObjectStore.AccessKey)
	assert.Equal(t, "foldex-change-me", cfg.ObjectStore.SecretKey)
	assert.Equal(t, "foldex-screenshots", cfg.ObjectStore.Bucket)
	assert.False(t, cfg.ObjectStore.UseSSL)
}

func TestLoad_ObjectStoreOverrides(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("RUSTFS_ENDPOINT", "rustfs:9000")
	t.Setenv("RUSTFS_ACCESS_KEY", "mykey")
	t.Setenv("RUSTFS_SECRET_KEY", "mysecret")
	t.Setenv("RUSTFS_BUCKET", "mybucket")
	t.Setenv("RUSTFS_USE_SSL", "true")
	// Legacy keys must lose to RUSTFS_*.
	t.Setenv("MINIO_ENDPOINT", "minio:9000")
	t.Setenv("MINIO_ACCESS_KEY", "old")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "rustfs:9000", cfg.ObjectStore.Endpoint)
	assert.Equal(t, "mykey", cfg.ObjectStore.AccessKey)
	assert.Equal(t, "mysecret", cfg.ObjectStore.SecretKey)
	assert.Equal(t, "mybucket", cfg.ObjectStore.Bucket)
	assert.True(t, cfg.ObjectStore.UseSSL)
}

func TestLoad_ObjectStoreLegacyMinIOFallback(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	for _, k := range []string{
		"RUSTFS_ENDPOINT", "RUSTFS_ACCESS_KEY", "RUSTFS_SECRET_KEY", "RUSTFS_BUCKET", "RUSTFS_USE_SSL",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("MINIO_ENDPOINT", "legacy:9000")
	t.Setenv("MINIO_ACCESS_KEY", "legacykey")
	t.Setenv("MINIO_SECRET_KEY", "legacysecret")
	t.Setenv("MINIO_BUCKET", "legacybucket")
	t.Setenv("MINIO_USE_SSL", "true")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "legacy:9000", cfg.ObjectStore.Endpoint)
	assert.Equal(t, "legacykey", cfg.ObjectStore.AccessKey)
	assert.Equal(t, "legacysecret", cfg.ObjectStore.SecretKey)
	assert.Equal(t, "legacybucket", cfg.ObjectStore.Bucket)
	assert.True(t, cfg.ObjectStore.UseSSL)
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

// normalizeAuth clamps rather than rejects: these are tuning values, and an
// operator who typos AUTH_ACCESS_TTL_MIN=0 should get the default, not a
// backend that refuses to boot.
func TestNormalizeAuthClampsTuningValues(t *testing.T) {
	c := Config{
		AuthAccessTTLMin:     0,
		AuthRefreshTTLDays:   -5,
		AuthAbsoluteTTLDays:  0,
		AuthRefreshGraceSec:  -1,
		AuthSweepIntervalMin: 0,
		AuthSweepRetainDays:  0,
	}
	c.normalizeAuth()

	assert.Equal(t, 15, c.AuthAccessTTLMin)
	assert.Equal(t, 30, c.AuthRefreshTTLDays)
	assert.Equal(t, 0, c.AuthRefreshGraceSec, "a negative grace must floor at 0, not stay negative")
	assert.Equal(t, 60, c.AuthSweepIntervalMin)
	assert.Equal(t, 7, c.AuthSweepRetainDays)
}

// The absolute ceiling exists to retire a refresh token that keeps rotating.
// If it were allowed below the sliding refresh TTL, the ceiling would be lower
// than the window it caps — the sessions would expire on the wrong clock.
func TestNormalizeAuthKeepsTheAbsoluteCeilingAboveTheSlidingWindow(t *testing.T) {
	c := Config{AuthRefreshTTLDays: 30, AuthAbsoluteTTLDays: 5}
	c.normalizeAuth()
	assert.GreaterOrEqual(t, c.AuthAbsoluteTTLDays, c.AuthRefreshTTLDays)

	c = Config{AuthRefreshTTLDays: 30, AuthAbsoluteTTLDays: 90}
	c.normalizeAuth()
	assert.Equal(t, 90, c.AuthAbsoluteTTLDays, "a sane value must be left alone")
}

// An unbounded grace window would let a genuinely stolen refresh token be
// replayed for as long as the window lasts.
func TestNormalizeAuthCapsTheGraceWindow(t *testing.T) {
	c := Config{AuthRefreshGraceSec: 86_400}
	c.normalizeAuth()
	assert.Equal(t, 60, c.AuthRefreshGraceSec)
}

// Cookie Secure is derived, never read from the environment: getting it wrong
// fails silently, because a browser drops a Secure cookie over plain HTTP
// without a word — login "succeeds" and the next request is anonymous.
func TestAuthCookieSecureIsDerivedFromTheBind(t *testing.T) {
	for _, bind := range []string{"127.0.0.1", "localhost", "::1", ""} {
		c := Config{BindAddr: bind}
		c.normalizeAuth()
		assert.False(t, c.AuthCookieSecure, "loopback bind %q runs plain HTTP in dev", bind)
	}
	for _, bind := range []string{"0.0.0.0", "10.0.0.5"} {
		c := Config{BindAddr: bind}
		c.normalizeAuth()
		assert.True(t, c.AuthCookieSecure, "network-reachable bind %q must set Secure", bind)
	}
}

// MAIL_INSECURE_SKIP_VERIFY aimed at a real host turns TLS into obfuscation:
// the connection is encrypted to whoever answered, which for an active attacker
// is them. The credential riding on it is the SMTP password; the payload is
// invite links.
func TestValidateSecureDefaultsRefusesInsecureMailAgainstARealHost(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1"}
	c.Mail.InsecureSkipVerify = true
	c.Mail.Host = "smtp.gmail.com"
	require.Error(t, c.validateSecureDefaults())

	// A local test server is the one legitimate use.
	c.Mail.Host = "localhost"
	require.NoError(t, c.validateSecureDefaults())

	c.Mail.Host = "127.0.0.1"
	require.NoError(t, c.validateSecureDefaults())

	// Off entirely is always fine.
	c.Mail.InsecureSkipVerify = false
	c.Mail.Host = "smtp.gmail.com"
	require.NoError(t, c.validateSecureDefaults())
}

// The issuer is the label an authenticator app shows next to the code. A user
// running two Foldex instances would otherwise get two indistinguishable
// entries and no way to tell which code belongs to which.
func TestIssuerFromURL(t *testing.T) {
	cases := map[string]string{
		"https://foldex.example.com":    "Foldex (foldex.example.com)",
		"http://localhost:9088":         "Foldex (localhost)", // the port is dropped
		"https://foldex.test/some/path": "Foldex (foldex.test)",
		"":                              "Foldex",
		"not a url":                     "Foldex",
		"::::":                          "Foldex",
	}
	for in, want := range cases {
		if got := issuerFromURL(in); got != want {
			t.Errorf("issuerFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A colon separates issuer from account in the otpauth:// label grammar, so one
// inside the issuer makes some apps display the account as part of the issuer.
func TestIssuerFromURLHasNoColon(t *testing.T) {
	for _, in := range []string{"https://foldex.test:8443", "http://[::1]:9088"} {
		got := issuerFromURL(in)
		if strings.Contains(strings.TrimPrefix(got, "Foldex "), ":") {
			t.Errorf("issuerFromURL(%q) = %q, which contains a colon", in, got)
		}
	}
}

func TestTwoFactorDefaults(t *testing.T) {
	t.Setenv("BACKEND_BIND", "127.0.0.1")
	t.Setenv("DB_URL", "postgres://u:p@localhost:5432/foldex?sslmode=disable")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Administrators can invite, promote and delete accounts, so the policy is
	// on unless an operator deliberately turns it off.
	if !cfg.AuthRequire2FAForAdmins {
		t.Error("AUTH_REQUIRE_2FA_FOR_ADMINS should default to true")
	}
	if cfg.AuthEncryptionKeyPath == "" {
		t.Error("the encryption key must have a default persistence path — it cannot be session-only")
	}
	if !cfg.AuthEncryptionAutoGen {
		t.Error("auto-generation should be on so a fresh install boots without manual key material")
	}
	if cfg.AuthTOTPIssuer == "" {
		t.Error("the issuer must be derived when not set explicitly")
	}
}

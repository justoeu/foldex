package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateSecureDefaults: non-loopback bind requires AUTH_ENABLED.
// CORS is irrelevant to the auth decision.
func TestValidateSecureDefaults(t *testing.T) {
	cases := []struct {
		name    string
		bind    string
		auth    bool
		cors    []string
		wantErr bool
	}{
		{"localhost loopback default", "127.0.0.1", false, []string{"*"}, false},
		{"loopback alias", "localhost", false, []string{"*"}, false},
		{"ipv6 loopback", "::1", false, []string{"*"}, false},
		{"empty bind is loopback", "", false, []string{"*"}, false},
		{"public bind + auth off + wildcard CORS", "0.0.0.0", false, []string{"*"}, true},
		{"public bind + LAN IP + auth off", "192.168.1.10", false, []string{"*"}, true},
		// Restricted CORS must NOT exempt missing auth on public bind:
		{"public bind + restricted CORS still needs auth", "0.0.0.0", false, []string{"https://example"}, true},
		{"public bind + multi-origin without auth", "0.0.0.0", false, []string{"https://a", "https://b"}, true},
		{"public bind + auth on", "0.0.0.0", true, []string{"*"}, false},
		{"public bind + auth on + restricted CORS", "0.0.0.0", true, []string{"https://example"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{BindAddr: tc.bind, AuthEnabled: tc.auth, CORSOrigins: tc.cors}
			err := c.validateSecureDefaults()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "insecure config")
				assert.Contains(t, err.Error(), "AUTH_ENABLED")
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

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "9089", cfg.Port)
	assert.Equal(t, 4, cfg.PreviewConcurrency)
	assert.Equal(t, 5, cfg.PreviewTimeoutSec)
	assert.Equal(t, []string{"*"}, cfg.CORSOrigins)
}

func TestLoad_Overrides(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("BACKEND_PORT", "9090")
	t.Setenv("PREVIEW_WORKER_CONCURRENCY", "8")
	t.Setenv("PREVIEW_FETCH_TIMEOUT_SEC", "10")
	t.Setenv("CORS_ORIGINS", "http://localhost:9088, https://foldex.example")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "9090", cfg.Port)
	assert.Equal(t, 8, cfg.PreviewConcurrency)
	assert.Equal(t, 10, cfg.PreviewTimeoutSec)
	assert.Equal(t, []string{"http://localhost:9088", "https://foldex.example"}, cfg.CORSOrigins)
}

func TestLoad_ClampsConcurrency(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("PREVIEW_WORKER_CONCURRENCY", "-3")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.PreviewConcurrency, "negative concurrency should be clamped to 1")
}

func TestLoad_ClampsWorkerConcurrencyAtResourceCeiling(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("PREVIEW_WORKER_CONCURRENCY", "100000")
	t.Setenv("CHANGECHECK_WORKER_CONCURRENCY", "100000")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 8, cfg.PreviewConcurrency)
	assert.Equal(t, 8, cfg.ChangeCheckConcurrency)
}

func TestLoad_UsesDefaultForNegativeChangeCheckConcurrency(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("CHANGECHECK_WORKER_CONCURRENCY", "-3")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 2, cfg.ChangeCheckConcurrency)
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
	assert.Empty(t, cfg.ObjectStore.SecretKey)
	assert.Equal(t, "foldex-screenshots", cfg.ObjectStore.Bucket)
	assert.False(t, cfg.ObjectStore.UseSSL)
}

func TestLoad_RejectsKnownRustFSPlaceholderSecrets(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x@y/z")
	t.Setenv("RUSTFS_ROOT_SECRET_KEY", "")
	t.Setenv("RUSTFS_SECRET_KEY", "foldex-change-me")
	t.Setenv("RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS", "")

	_, err := Load()
	require.ErrorContains(t, err, "placeholder")
	assert.NotContains(t, err.Error(), "foldex-change-me")

	t.Setenv("RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS", "1")
	_, err = Load()
	require.NoError(t, err)
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
	// normalizeAuth reads AUTH_PUBLIC_URL from the ENVIRONMENT rather than from
	// the struct — deliberately, since what decides Secure is the origin the
	// browser talks to. That makes this case about the FALLBACK, and the
	// fallback only runs when the variable is absent. Without clearing it the
	// test inherits whatever the developer exported: `backend/Makefile` does
	// `include ../.env` + `export`, so a complete .env turns this red on a
	// laptop while CI — which has no .env — stays green. A failure that only
	// reproduces on the machine of whoever is mid-change reads as "my patch
	// broke it" and costs an afternoon.
	t.Setenv("AUTH_PUBLIC_URL", "")

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

// A test about DEFAULTS has to isolate itself from the ambient environment.
//
// backend/Makefile includes ../.env and exports it, so `make coverage-run` runs
// with the operator's real configuration in scope — and the moment someone sets
// one of these in their own .env, a test that reads the ambient value starts
// asserting their config instead of the default. It failed exactly that way the
// first time an AUTH_ENCRYPTION_AUTO_GENERATE=0 landed in .env. Note it would
// still have passed in CI, which has no .env: a local-only failure is worse
// than a loud one, because it looks like a broken checkout.
//
// envOr/envBool treat "" as absent, so clearing is enough to reach the default.
func TestTwoFactorDefaults(t *testing.T) {
	for _, k := range []string{
		"AUTH_REQUIRE_2FA_FOR_ADMINS", "AUTH_ENCRYPTION_KEY", "AUTH_ENCRYPTION_KEY_PATH",
		"AUTH_ENCRYPTION_AUTO_GENERATE", "AUTH_TOTP_ISSUER", "AUTH_PUBLIC_URL",
	} {
		t.Setenv(k, "")
	}
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

// The redirect URI is DERIVED, never configured. It must match what is
// registered in the Google Cloud console byte for byte — Google's
// redirect_uri_mismatch says nothing about which side is wrong — so the one
// thing worth locking is that a trailing slash on AUTH_PUBLIC_URL does not
// silently produce a double slash.
func TestGoogleRedirectURL(t *testing.T) {
	for _, tc := range []struct{ public, want string }{
		{"https://foldex.example", "https://foldex.example/api/auth/oauth/google/callback"},
		{"https://foldex.example/", "https://foldex.example/api/auth/oauth/google/callback"},
		{"https://foldex.example///", "https://foldex.example/api/auth/oauth/google/callback"},
		{"http://localhost:9088", "http://localhost:9088/api/auth/oauth/google/callback"},
	} {
		got := Config{AuthPublicURL: tc.public}.GoogleRedirectURL()
		if got != tc.want {
			t.Errorf("GoogleRedirectURL(%q) = %q, want %q", tc.public, got, tc.want)
		}
	}
}

func TestLoad_GoogleOAuthIsOffByDefault(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x")
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GoogleClientID != "" || cfg.GoogleClientSecret != "" {
		t.Error("Google OAuth must stay unconfigured unless both values are set")
	}
	// Numeric share links are off by default: /go/{id} and /n/{id} resolve with
	// no session, and link ids are a counter shared across every account.
	if cfg.PublicNumericIDs {
		t.Error("PUBLIC_NUMERIC_IDS must default to false")
	}
	// And accounts are ON, since PR4.
	if !cfg.AuthEnabled {
		t.Error("AUTH_ENABLED must default to true")
	}
}

func TestLoad_PublicNumericIDsOptIn(t *testing.T) {
	t.Setenv("DB_URL", "postgres://x")
	t.Setenv("PUBLIC_NUMERIC_IDS", "1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PublicNumericIDs {
		t.Error("PUBLIC_NUMERIC_IDS=1 must re-enable the legacy numeric form")
	}
}

// The shipped compose stack binds 0.0.0.0 so nginx can reach the backend.
// AUTH_ENABLED answers the "can anyone who reaches the port read the data"
// question properly: it identifies the caller and scopes every row to them.
func TestValidateSecureDefaults_AuthSatisfiesTheNonLoopbackGuard(t *testing.T) {
	shipped := Config{BindAddr: "0.0.0.0", AuthEnabled: true}
	if err := shipped.validateSecureDefaults(); err != nil {
		t.Fatalf("the shipped compose configuration must boot: %v", err)
	}

	// Turning auth OFF on that same bind is the case worth refusing: every
	// request would be attributed to the bootstrap administrator, so anyone who
	// reaches the port owns the library.
	unguarded := Config{BindAddr: "0.0.0.0", AuthEnabled: false}
	if err := unguarded.validateSecureDefaults(); err == nil {
		t.Error("AUTH_ENABLED=0 on a network bind must be refused")
	}
}

// The cookie Secure flag is decided by the scheme of the origin THE BROWSER
// talks to, not by how the backend binds. The two disagree in the topology this
// project recommends — loopback bind, nginx terminating TLS — and the old
// bind-only derivation got that case wrong, shipping session cookies with no
// Secure flag to a browser on HTTPS.
func TestLoad_CookieSecureFollowsThePublicURLScheme(t *testing.T) {
	for _, tc := range []struct {
		name      string
		publicURL string
		bind      string
		want      bool
	}{
		{
			// The regression this exists for: the documented reverse-proxy
			// deployment. Bind says "local"; the browser is on HTTPS.
			name:      "loopback bind behind an https proxy is Secure",
			publicURL: "https://foldex.example", bind: "127.0.0.1", want: true,
		},
		{
			// And the mirror image: reachable bind, but the operator genuinely
			// serves plain HTTP. A Secure cookie here is dropped in silence.
			name:      "network bind serving plain http is not Secure",
			publicURL: "http://192.168.1.10:9089", bind: "0.0.0.0", want: false,
		},
		{
			name:      "the shipped compose origin is Secure",
			publicURL: "https://localhost:9444", bind: "0.0.0.0", want: true,
		},
		{
			// No public URL configured: fall back to the bind heuristic, which
			// is the plain-HTTP dev server the footgun warning is about.
			name:      "no public url falls back to the bind",
			publicURL: "", bind: "127.0.0.1", want: false,
		},
		{
			// The regression guard. AuthPublicURL carries a default of
			// http://localhost:9088, so deriving from the DEFAULTED field
			// instead of the environment would turn Secure off for every
			// deployment that binds 0.0.0.0 without configuring a public URL —
			// including the shipped compose stack with the value left unset.
			name:      "no public url with a reachable bind falls back to Secure",
			publicURL: "", bind: "0.0.0.0", want: true,
		},
		{
			// A malformed value must not decide cookie policy from a scheme
			// nobody meant to set.
			name:      "a relative public url falls back to the bind",
			publicURL: "/app", bind: "0.0.0.0", want: true,
		},
		{
			name:      "a non-http scheme falls back to the bind",
			publicURL: "ftp://foldex.example", bind: "127.0.0.1", want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DB_URL", "postgres://x@y/z")
			t.Setenv("AUTH_PUBLIC_URL", tc.publicURL)
			t.Setenv("BACKEND_BIND", tc.bind)

			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.AuthCookieSecure)
		})
	}
}

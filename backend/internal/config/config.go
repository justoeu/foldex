package config

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// MailConfig holds transactional-mail wiring (internal/mailer).
//
// Driver defaults to "log", which writes the message body — invite link
// included — to the structured log instead of sending it. That is the right
// default for a self-hosted product: an operator who never configures SMTP
// must still be able to complete an invite flow.
type MailConfig struct {
	Driver             string
	Host               string
	Port               int
	Username           string
	Password           string
	From               string
	FromName           string
	STARTTLS           bool
	TLS                bool
	InsecureSkipVerify bool
}

// ObjectStoreConfig holds S3-compatible object-storage parameters (RustFS).
type ObjectStoreConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

type Config struct {
	Port               string
	BindAddr           string // listen address; default 127.0.0.1 (single-user threat model)
	DBURL              string
	PreviewConcurrency int
	PreviewTimeoutSec  int
	SharedSecret       string
	CORSOrigins        []string

	// AuthEnabled turns on the multi-user authentication stack (ADR-30).
	//
	// Defaults to TRUE since PR4. On an upgraded install the first visit lands
	// on the setup screen, which CLAIMS the placeholder admin migration 000017
	// created — the row every pre-existing link, note, folder and tag was
	// adopted into — so the operator's library is intact behind their new
	// account rather than stranded under an unreachable pending row.
	//
	// Setting it to 0 keeps the old behaviour: the router injects the bootstrap
	// admin as the principal for every request and nothing ever asks for a
	// password. That is a real escape hatch for a machine on a private network
	// with one user, not a deprecated flag.
	AuthEnabled bool

	// AuthPublicURL is the origin baked into invite links.
	//
	// It cannot be derived from the request: Host and X-Forwarded-Host are
	// attacker-supplied, and building a credential-bearing link from them is
	// the classic reset-poisoning primitive — the mail reaches the real user
	// but points at the attacker's host.
	AuthPublicURL string
	// AuthCookieSecure marks session cookies HTTPS-only. It defaults to the
	// inverse of a loopback bind: a local dev server on plain HTTP must not set
	// Secure (the browser silently drops the cookie and login appears to
	// succeed then immediately forget), while anything network-reachable must.
	AuthCookieSecure bool
	AuthCookieDomain string

	AuthAccessTTLMin     int
	AuthRefreshTTLDays   int
	AuthAbsoluteTTLDays  int
	AuthRefreshGraceSec  int
	AuthSweepIntervalMin int
	AuthSweepRetainDays  int

	// AuthEncryptionKey (base64) encrypts TOTP seeds at rest. Unlike the
	// folder-unlock key, this one CANNOT be regenerated: losing it makes every
	// stored seed undecryptable and locks every 2FA user out permanently. That
	// is why the path variable is mandatory when auto-generating — see
	// internal/pkg/keyfile's AllowEphemeral.
	AuthEncryptionKey     string
	AuthEncryptionKeyPath string
	AuthEncryptionAutoGen bool

	// AuthTOTPIssuer is the label authenticator apps display. It defaults to
	// the public URL's host so a user with two foldex instances can tell the
	// two entries apart.
	AuthTOTPIssuer string

	// TrustedProxyIPs (CSV of IPs or CIDRs) is the set of peers whose
	// X-Forwarded-For header may be believed. Empty means "believe nobody",
	// which is the right default for the loopback bind that ships: a spoofable
	// client address is worse than a coarse one, because it lets an attacker
	// both evade their own rate-limit bucket and pin the cost on someone else.
	TrustedProxyIPs string

	// AuthRequire2FAForAdmins forces administrators through TOTP enrollment
	// before they can use the app. An admin can invite, promote and delete
	// accounts, so a stolen admin password should not be one factor away from
	// the whole instance.
	AuthRequire2FAForAdmins bool

	// Google OAuth (ADR-31). All three are required together; with any of them
	// empty the provider reports itself disabled and the routes answer
	// "not configured" instead of failing halfway through a redirect.
	//
	// The redirect URI is DERIVED from AuthPublicURL rather than configured
	// separately, so it cannot drift from the origin the invite and reset links
	// already use — and so an operator cannot point it at a host they do not
	// control.
	GoogleClientID     string
	GoogleClientSecret string

	// PublicNumericIDs re-enables the legacy /go/{42} and /n/{42} forms (ADR-32).
	// Off by default: both routes resolve without a session, so with content
	// now shared across accounts a dense BIGSERIAL in the path lets anyone
	// enumerate — and click-log — every link and note on the instance. The
	// escape hatch exists for instances with old /go/42 links already shared.
	PublicNumericIDs bool

	Mail        MailConfig
	ObjectStore ObjectStoreConfig

	// Change-check worker (internal/changecheck). Per-link opt-in, runs
	// hourly/daily/weekly diffs and fires Web Push notifications.
	ChangeCheckEnabled         bool
	ChangeCheckConcurrency     int
	ChangeCheckScanIntervalSec int
	ChangeCheckFetchTimeoutSec int

	// Web Push / VAPID (internal/push). When all three VAPID values are
	// empty and VAPID_AUTO_GENERATE=1 (default), the push package generates
	// and persists a keypair under /data/vapid.json on first boot.
	VAPIDPublicKey    string
	VAPIDPrivateKey   string
	VAPIDSubject      string
	VAPIDAutoGenerate bool
	VAPIDStatePath    string

	// Folder-unlock-token HMAC secret (internal/folders). Same env→file→
	// autogen shape as VAPID above — see folders.LoadOrGenerateFolderUnlockKey.
	FolderUnlockKey          string
	FolderUnlockKeyPath      string
	FolderUnlockAutoGenerate bool
}

func Load() (Config, error) {
	cfg := Config{
		Port:               envOr("BACKEND_PORT", "9089"),
		BindAddr:           envOr("BACKEND_BIND", "127.0.0.1"),
		DBURL:              os.Getenv("DB_URL"),
		PreviewConcurrency: envInt("PREVIEW_WORKER_CONCURRENCY", 4),
		PreviewTimeoutSec:  envInt("PREVIEW_FETCH_TIMEOUT_SEC", 5),
		SharedSecret:       os.Getenv("SHARED_SECRET"),
		CORSOrigins:        splitCSV(envOr("CORS_ORIGINS", "*")),
		AuthEnabled:        envBool("AUTH_ENABLED", true),
		AuthPublicURL:      envOr("AUTH_PUBLIC_URL", "http://localhost:9088"),
		AuthCookieDomain:   os.Getenv("AUTH_COOKIE_DOMAIN"),
		// Clamped in Load(): see normalizeAuth.
		AuthAccessTTLMin:     envInt("AUTH_ACCESS_TTL_MIN", 15),
		AuthRefreshTTLDays:   envInt("AUTH_REFRESH_TTL_DAYS", 30),
		AuthAbsoluteTTLDays:  envInt("AUTH_ABSOLUTE_TTL_DAYS", 90),
		AuthRefreshGraceSec:  envInt("AUTH_REFRESH_GRACE_SEC", 10),
		AuthSweepIntervalMin: envInt("AUTH_SWEEP_INTERVAL_MIN", 60),
		AuthSweepRetainDays:  envInt("AUTH_SWEEP_RETAIN_DAYS", 7),

		AuthEncryptionKey:     os.Getenv("AUTH_ENCRYPTION_KEY"),
		AuthEncryptionKeyPath: envOr("AUTH_ENCRYPTION_KEY_PATH", "/data/auth_encryption.key"),
		AuthEncryptionAutoGen: envBool("AUTH_ENCRYPTION_AUTO_GENERATE", true),
		AuthTOTPIssuer:        os.Getenv("AUTH_TOTP_ISSUER"),

		TrustedProxyIPs:         os.Getenv("TRUSTED_PROXY_IPS"),
		AuthRequire2FAForAdmins: envBool("AUTH_REQUIRE_2FA_FOR_ADMINS", true),
		GoogleClientID:          os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret:      os.Getenv("GOOGLE_CLIENT_SECRET"),
		PublicNumericIDs:        envBool("PUBLIC_NUMERIC_IDS", false),
		Mail: MailConfig{
			Driver:             envOr("MAIL_DRIVER", "log"),
			Host:               os.Getenv("MAIL_HOST"),
			Port:               envInt("MAIL_PORT", 587),
			Username:           os.Getenv("MAIL_USERNAME"),
			Password:           os.Getenv("MAIL_PASSWORD"),
			From:               envOr("MAIL_FROM", "foldex@localhost"),
			FromName:           envOr("MAIL_FROM_NAME", "Foldex"),
			STARTTLS:           envBool("MAIL_STARTTLS", true),
			TLS:                envBool("MAIL_TLS", false),
			InsecureSkipVerify: envBool("MAIL_INSECURE_SKIP_VERIFY", false),
		},
		ObjectStore: ObjectStoreConfig{
			// RUSTFS_* is canonical. MINIO_* is accepted as a one-release
			// migration fallback so existing .env files keep working.
			Endpoint:  envFirst("RUSTFS_ENDPOINT", "MINIO_ENDPOINT", "localhost:9000"),
			AccessKey: envFirst("RUSTFS_ACCESS_KEY", "MINIO_ACCESS_KEY", "foldex"),
			SecretKey: envFirst("RUSTFS_SECRET_KEY", "MINIO_SECRET_KEY", "foldex-change-me"),
			Bucket:    envFirst("RUSTFS_BUCKET", "MINIO_BUCKET", "foldex-screenshots"),
			UseSSL:    envBoolFirst("RUSTFS_USE_SSL", "MINIO_USE_SSL", false),
		},
		ChangeCheckEnabled:         envBool("CHANGECHECK_ENABLED", true),
		ChangeCheckConcurrency:     envInt("CHANGECHECK_WORKER_CONCURRENCY", 2),
		ChangeCheckScanIntervalSec: envInt("CHANGECHECK_SCAN_INTERVAL_SEC", 60),
		ChangeCheckFetchTimeoutSec: envInt("CHANGECHECK_FETCH_TIMEOUT_SEC", 20),
		VAPIDPublicKey:             os.Getenv("VAPID_PUBLIC_KEY"),
		VAPIDPrivateKey:            os.Getenv("VAPID_PRIVATE_KEY"),
		VAPIDSubject:               envOr("VAPID_SUBJECT", "mailto:foldex@localhost"),
		VAPIDAutoGenerate:          envBool("VAPID_AUTO_GENERATE", true),
		VAPIDStatePath:             envOr("VAPID_STATE_PATH", "/data/vapid.json"),
		FolderUnlockKey:            os.Getenv("FOLDER_UNLOCK_KEY"),
		FolderUnlockKeyPath:        envOr("FOLDER_UNLOCK_KEY_PATH", "/data/folder_unlock.key"),
		FolderUnlockAutoGenerate:   envBool("FOLDER_UNLOCK_AUTO_GENERATE", true),
	}
	if cfg.DBURL == "" {
		return cfg, errors.New("DB_URL is required")
	}
	if cfg.PreviewConcurrency < 1 {
		cfg.PreviewConcurrency = 1
	}
	cfg.normalizeAuth()
	if err := cfg.validateSecureDefaults(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// normalizeAuth clamps the session knobs and derives the cookie Secure flag.
//
// Clamping rather than rejecting: these are tuning values, and an operator who
// typos AUTH_ACCESS_TTL_MIN=0 should get the default, not a backend that
// refuses to boot. The one value with a security floor is the absolute TTL,
// which must exceed the sliding refresh TTL or the ceiling it exists to impose
// would be lower than the window it caps.
func (c *Config) normalizeAuth() {
	if c.AuthAccessTTLMin < 1 {
		c.AuthAccessTTLMin = 15
	}
	if c.AuthRefreshTTLDays < 1 {
		c.AuthRefreshTTLDays = 30
	}
	if c.AuthAbsoluteTTLDays < c.AuthRefreshTTLDays {
		c.AuthAbsoluteTTLDays = c.AuthRefreshTTLDays
	}
	// A negative grace would classify every racing tab as a replay and sign the
	// user out at random; an unbounded one would let a genuinely stolen token be
	// replayed indefinitely.
	if c.AuthRefreshGraceSec < 0 {
		c.AuthRefreshGraceSec = 0
	}
	if c.AuthRefreshGraceSec > 60 {
		c.AuthRefreshGraceSec = 60
	}
	if c.AuthSweepIntervalMin < 1 {
		c.AuthSweepIntervalMin = 60
	}
	if c.AuthSweepRetainDays < 1 {
		c.AuthSweepRetainDays = 7
	}

	// Secure is not read from the environment. Getting it wrong is a silent,
	// baffling failure — a Secure cookie over plain HTTP is dropped by the
	// browser without a word, so login "succeeds" and the very next request is
	// anonymous. Deriving it from the bind removes the footgun: loopback (dev,
	// plain HTTP) gets Secure=false, anything network-reachable gets true.
	c.AuthCookieSecure = !isLocalBind(c.BindAddr)

	// The issuer is what an authenticator app shows next to the code. Falling
	// back to the public URL's host beats a hardcoded "Foldex": a user running
	// two instances would otherwise get two indistinguishable entries and no
	// way to tell which code belongs to which.
	if c.AuthTOTPIssuer == "" {
		c.AuthTOTPIssuer = issuerFromURL(c.AuthPublicURL)
	}
}

// GoogleRedirectURL is the callback URI, derived from the public origin.
//
// Derived rather than configured so it cannot drift from the origin the invite
// and reset links already use. This exact string must also be registered in the
// Google Cloud console — Google compares it byte for byte, and a trailing slash
// or an http/https mismatch produces a redirect_uri_mismatch that says nothing
// about which of the two sides is wrong.
func (c Config) GoogleRedirectURL() string {
	return strings.TrimRight(c.AuthPublicURL, "/") + "/api/auth/oauth/google/callback"
}

// issuerFromURL reduces a public URL to a display label.
func issuerFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "Foldex"
	}
	host := u.Hostname()
	if host == "" {
		return "Foldex"
	}
	// A colon in the issuer breaks the otpauth:// label grammar, which uses it
	// as the issuer/account separator — some apps then display the account name
	// as part of the issuer. Hostname() already dropped the port; this guards
	// the remaining exotic cases.
	return "Foldex (" + strings.ReplaceAll(host, ":", "-") + ")"
}

// validateSecureDefaults refuses to boot when the API would be network-
// reachable without SHARED_SECRET. CORS is NOT authentication — a restricted
// origin list does not stop curl/scripts from hitting the API.
//
// Loopback binds (127.0.0.1 / ::1 / localhost) may omit SHARED_SECRET for the
// single-user local threat model. Any non-loopback bind requires a non-empty
// SHARED_SECRET.
func (c Config) validateSecureDefaults() error {
	if !isLocalBind(c.BindAddr) && c.SharedSecret == "" {
		return errors.New(
			"insecure config: BACKEND_BIND=" + c.BindAddr +
				" (non-loopback) AND SHARED_SECRET is empty — " +
				"set SHARED_SECRET, or bind to 127.0.0.1",
		)
	}
	// MAIL_INSECURE_SKIP_VERIFY exists for a self-signed dev SMTP server. Aimed
	// at a real host it silently turns TLS into obfuscation: the connection is
	// encrypted to whoever answered, which for an active attacker is them. The
	// credential riding on it is the SMTP password, and the payload is invite
	// and password-reset links.
	if c.Mail.InsecureSkipVerify && !isLocalBind(c.Mail.Host) {
		return errors.New(
			"insecure config: MAIL_INSECURE_SKIP_VERIFY=1 with MAIL_HOST=" + c.Mail.Host +
				" (non-loopback) — certificate verification may only be disabled for a local test server",
		)
	}
	return nil
}

func isLocalBind(addr string) bool {
	switch addr {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

// envFirst returns the first non-empty env among keys, else def.
func envFirst(primary, legacy, def string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	if v := os.Getenv(legacy); v != "" {
		return v
	}
	return def
}

func envBoolFirst(primary, legacy string, def bool) bool {
	if v := os.Getenv(primary); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	if v := os.Getenv(legacy); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "TRUE" || v == "yes"
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

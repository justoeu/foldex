package config

import (
	"errors"
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
	// Defaults to FALSE through PR3. With it off the router injects the
	// bootstrap admin as the principal for every request, so a single-user
	// deployment keeps working exactly as before while the segmentation work
	// lands. PR4 flips the default to true.
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
		AuthEnabled:        envBool("AUTH_ENABLED", false),
		AuthPublicURL:      envOr("AUTH_PUBLIC_URL", "http://localhost:9088"),
		AuthCookieDomain:   os.Getenv("AUTH_COOKIE_DOMAIN"),
		// Clamped in Load(): see normalizeAuth.
		AuthAccessTTLMin:     envInt("AUTH_ACCESS_TTL_MIN", 15),
		AuthRefreshTTLDays:   envInt("AUTH_REFRESH_TTL_DAYS", 30),
		AuthAbsoluteTTLDays:  envInt("AUTH_ABSOLUTE_TTL_DAYS", 90),
		AuthRefreshGraceSec:  envInt("AUTH_REFRESH_GRACE_SEC", 10),
		AuthSweepIntervalMin: envInt("AUTH_SWEEP_INTERVAL_MIN", 60),
		AuthSweepRetainDays:  envInt("AUTH_SWEEP_RETAIN_DAYS", 7),
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

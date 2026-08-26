// Package backupagent implements the operational backup agent (ADR-43,
// docs/SDD-OPS-BACKUP.md): scheduled pg_dump of the whole database, encrypted
// with age and shipped to an external S3-compatible bucket, with state and
// history in the backup_run table.
//
// It runs as its own binary (cmd/backup-agent) in its own container. The
// separation is the security design, not a packaging detail: this is the only
// process that holds the external bucket's credentials, and the web-exposed
// backend can only ever INSERT a 'requested' row for it to pick up.
package backupagent

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config carries everything the agent needs. Postgres connection details come
// from POSTGRES_* (INV-101: DB_URL is derived, never a second source of truth);
// everything agent-specific lives under the BACKUP_* namespace.
type Config struct {
	// Postgres (source).
	PGHost     string
	PGPort     int
	PGUser     string
	PGPassword string
	PGDatabase string
	PGSSLMode  string

	// External S3 target.
	S3Endpoint  string
	S3Region    string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool

	// RustFS source (mirror and user_zip jobs). Same RUSTFS_* names the
	// backend reads — one spelling per credential across the stack, because
	// it IS the same bucket (INV-101's spirit).
	RustFSEndpoint  string
	RustFSAccessKey string
	RustFSSecretKey string
	RustFSBucket    string
	RustFSUseSSL    bool

	// Scheduling.
	DumpAt            Anchor // zero Anchor = job disabled
	DrillAt           Anchor // restore drill; zero Anchor = job disabled
	UserZipAt         Anchor // per-user product ZIPs; zero Anchor = job disabled
	MirrorIntervalMin int    // 0 = mirror off
	RequestedPollSec  int
	StaleRunMin       int

	// Retention (GFS).
	RetainDaily   int
	RetainWeekly  int
	RetainMonthly int
	RetainUserZip int    // newest N archives kept per user (user_zip)
	RetentionMode string // "agent" | "bucket"

	// Encryption.
	AgeRecipients  []string
	AllowPlaintext bool
	// AgeIdentityFile is the private identity the drill decrypts with — the
	// only secret this agent holds beyond the S3 credentials. keyfile
	// posture: no autogenerate, no ephemeral fallback — a generated key that
	// only exists next to the data is an undecryptable backup on the day the
	// host dies.
	AgeIdentityFile string

	// SpoolDir is where the dump ciphertext is staged before upload. Empty
	// means the OS temp dir — which in the container is the writable LAYER,
	// fine for small libraries but worth pointing at a volume when the dump
	// outgrows the host's docker filesystem.
	SpoolDir string

	// Observability.
	MetricsAddr  string
	MetricsToken string

	// Version is FOLDEX_VERSION as the process sees it, reported in the
	// heartbeat so the band can say which agent build is running. Empty is a
	// gap, not an error — same posture as the backend's span version.
	Version string
}

// DBURL derives the pgx connection string from POSTGRES_* (INV-101).
// Built with url.URL, not QueryEscape: userinfo has its own escaping rules —
// QueryEscape turns a space into "+", which net/url does NOT decode back in
// userinfo, so a password with a space would fail auth with nothing pointing
// at the cause.
func (c Config) DBURL() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.PGUser, c.PGPassword),
		Host:     fmt.Sprintf("%s:%d", c.PGHost, c.PGPort),
		Path:     "/" + c.PGDatabase,
		RawQuery: "sslmode=" + url.QueryEscape(c.PGSSLMode),
	}
	return u.String()
}

// Load reads the agent configuration from the environment and validates it
// fail-fast: a half-configured backup agent that boots anyway is exactly the
// silent non-backup this design exists to kill.
func Load() (Config, error) {
	c := Config{
		PGHost:     envOr("POSTGRES_HOST", "db"),
		PGPort:     envInt("POSTGRES_PORT", 5432),
		PGUser:     envOr("POSTGRES_USER", "foldex"),
		PGPassword: os.Getenv("POSTGRES_PASSWORD"),
		PGDatabase: envOr("POSTGRES_DB", "foldex"),
		PGSSLMode:  envOr("POSTGRES_SSLMODE", "disable"),

		S3Endpoint:  os.Getenv("BACKUP_S3_ENDPOINT"),
		S3Region:    envOr("BACKUP_S3_REGION", "us-east-1"),
		S3Bucket:    os.Getenv("BACKUP_S3_BUCKET"),
		S3AccessKey: os.Getenv("BACKUP_S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("BACKUP_S3_SECRET_KEY"),
		S3UseSSL:    envBool("BACKUP_S3_USE_SSL", true),

		RustFSEndpoint:  envOr("RUSTFS_ENDPOINT", "rustfs:9000"),
		RustFSAccessKey: envOr("RUSTFS_ACCESS_KEY", "foldex"),
		RustFSSecretKey: os.Getenv("RUSTFS_SECRET_KEY"),
		RustFSBucket:    envOr("RUSTFS_BUCKET", "foldex-screenshots"),
		RustFSUseSSL:    envBool("RUSTFS_USE_SSL", false),

		MirrorIntervalMin: envInt("BACKUP_MIRROR_INTERVAL_MIN", 360),
		RequestedPollSec:  envInt("BACKUP_REQUESTED_POLL_SEC", 30),
		StaleRunMin:       envInt("BACKUP_STALE_RUN_MIN", 240),

		RetainDaily:   envInt("BACKUP_RETAIN_DAILY", 7),
		RetainWeekly:  envInt("BACKUP_RETAIN_WEEKLY", 4),
		RetainMonthly: envInt("BACKUP_RETAIN_MONTHLY", 6),
		RetainUserZip: envInt("BACKUP_RETAIN_USERZIP", 7),
		RetentionMode: envOr("BACKUP_RETENTION_MODE", "agent"),

		AllowPlaintext: envBool("BACKUP_ALLOW_PLAINTEXT", false),

		AgeIdentityFile: strings.TrimSpace(os.Getenv("BACKUP_AGE_IDENTITY_FILE")),

		SpoolDir: os.Getenv("BACKUP_SPOOL_DIR"),

		MetricsAddr:  envOr("BACKUP_METRICS_ADDR", ":9099"),
		Version:      os.Getenv("FOLDEX_VERSION"),
		MetricsToken: os.Getenv("METRICS_TOKEN"),
	}

	for _, missing := range []struct{ name, val string }{
		{"BACKUP_S3_ENDPOINT", c.S3Endpoint},
		{"BACKUP_S3_BUCKET", c.S3Bucket},
		{"BACKUP_S3_ACCESS_KEY", c.S3AccessKey},
		{"BACKUP_S3_SECRET_KEY", c.S3SecretKey},
	} {
		if strings.TrimSpace(missing.val) == "" {
			return Config{}, fmt.Errorf("backupagent: %s is required — the backup agent refuses to boot half-configured", missing.name)
		}
	}

	// The mirror is on by default (SDD §4) and reads the RustFS origin, so
	// its credentials are required exactly while it is on: a mirror that
	// silently never copies is the mailer incident again. An instance with no
	// object store turns the job off explicitly.
	if c.MirrorEnabled() {
		for _, missing := range []struct{ name, val string }{
			{"RUSTFS_ENDPOINT", c.RustFSEndpoint},
			{"RUSTFS_ACCESS_KEY", c.RustFSAccessKey},
			{"RUSTFS_SECRET_KEY", c.RustFSSecretKey},
			{"RUSTFS_BUCKET", c.RustFSBucket},
		} {
			if strings.TrimSpace(missing.val) == "" {
				return Config{}, fmt.Errorf("backupagent: %s is required while the mirror job is enabled — set it, or set BACKUP_MIRROR_INTERVAL_MIN=0 to turn the mirror off", missing.name)
			}
		}
	}

	if raw := strings.TrimSpace(os.Getenv("BACKUP_DUMP_AT")); raw != "" {
		anchor, err := ParseAnchor(raw)
		if err != nil {
			return Config{}, fmt.Errorf("backupagent: BACKUP_DUMP_AT: %w", err)
		}
		c.DumpAt = anchor
	}
	if raw := strings.TrimSpace(os.Getenv("BACKUP_DRILL_AT")); raw != "" {
		anchor, err := ParseAnchor(raw)
		if err != nil {
			return Config{}, fmt.Errorf("backupagent: BACKUP_DRILL_AT: %w", err)
		}
		c.DrillAt = anchor
	}

	if raw := strings.TrimSpace(os.Getenv("BACKUP_USERZIP_AT")); raw != "" {
		anchor, err := ParseAnchor(raw)
		if err != nil {
			return Config{}, fmt.Errorf("backupagent: BACKUP_USERZIP_AT: %w", err)
		}
		c.UserZipAt = anchor
	}
	// The user_zip Export reads the caller's objects from the SOURCE bucket,
	// so opting into the job without its credentials is the same half-
	// configured boot the S3 checks above refuse.
	if c.UserZipAt.Enabled() && strings.TrimSpace(c.RustFSSecretKey) == "" {
		return Config{}, fmt.Errorf("backupagent: BACKUP_USERZIP_AT is set but RUSTFS_SECRET_KEY is empty — user_zip reads the source bucket and refuses to boot half-configured")
	}

	if c.RetentionMode != "agent" && c.RetentionMode != "bucket" {
		return Config{}, fmt.Errorf("backupagent: BACKUP_RETENTION_MODE must be \"agent\" or \"bucket\", got %q — the mode is declared, never inferred from an AccessDenied", c.RetentionMode)
	}

	if recips := strings.TrimSpace(os.Getenv("BACKUP_AGE_RECIPIENTS")); recips != "" {
		for _, r := range strings.Split(recips, ",") {
			if r = strings.TrimSpace(r); r != "" {
				c.AgeRecipients = append(c.AgeRecipients, r)
			}
		}
	}
	// The dump carries bcrypt hashes and every user's content, and the target
	// is a bucket off the machine. Plaintext is an explicit, named opt-out for
	// operators who encrypt at the bucket (SSE-KMS) — never a fallback.
	if len(c.AgeRecipients) == 0 && !c.AllowPlaintext {
		return Config{}, fmt.Errorf("backupagent: BACKUP_AGE_RECIPIENTS is empty: refusing to upload plaintext dumps (set it, or set BACKUP_ALLOW_PLAINTEXT=1 if the bucket itself encrypts)")
	}

	// A scheduled drill over encrypted dumps without the identity would boot
	// fine and fail every week at 04:30 — the silent non-backup shape again.
	// Fail at boot, naming the knob.
	if c.DrillAt.Enabled() && len(c.AgeRecipients) > 0 && c.AgeIdentityFile == "" {
		return Config{}, fmt.Errorf("backupagent: BACKUP_DRILL_AT is set but BACKUP_AGE_IDENTITY_FILE is empty — the drill cannot decrypt the dumps it must restore (mount the private age identity read-only, mode 0600)")
	}

	return c, nil
}

// Duration helpers keep call sites honest about units.
func (c Config) RequestedPoll() time.Duration { return time.Duration(c.RequestedPollSec) * time.Second }
func (c Config) StaleRunTTL() time.Duration   { return time.Duration(c.StaleRunMin) * time.Minute }
func (c Config) MirrorInterval() time.Duration {
	return time.Duration(c.MirrorIntervalMin) * time.Minute
}

// MirrorEnabled reports whether the mirror job has a cadence at all.
func (c Config) MirrorEnabled() bool { return c.MirrorIntervalMin > 0 }

// HasRustFSCreds reports whether the source-bucket credentials are present —
// the CAPABILITY gate for user_zip (ADR-44): with credentials the binary
// builds the job and the schedule (env anchor or a backup_schedule row)
// decides when it runs; without them no row can switch it on.
func (c Config) HasRustFSCreds() bool {
	return strings.TrimSpace(c.RustFSEndpoint) != "" &&
		strings.TrimSpace(c.RustFSAccessKey) != "" &&
		strings.TrimSpace(c.RustFSSecretKey) != ""
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	default:
		return def
	}
}

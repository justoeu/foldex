package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

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
	ObjectStore        ObjectStoreConfig

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
	if err := cfg.validateSecureDefaults(); err != nil {
		return cfg, err
	}
	return cfg, nil
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

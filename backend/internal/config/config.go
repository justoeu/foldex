package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// MinIOConfig holds the object-storage connection parameters.
type MinIOConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

type Config struct {
	Port               string
	DBURL              string
	PreviewConcurrency int
	PreviewTimeoutSec  int
	SharedSecret       string
	CORSOrigins        []string
	MinIO              MinIOConfig
}

func Load() (Config, error) {
	cfg := Config{
		Port:               envOr("BACKEND_PORT", "9089"),
		DBURL:              os.Getenv("DB_URL"),
		PreviewConcurrency: envInt("PREVIEW_WORKER_CONCURRENCY", 4),
		PreviewTimeoutSec:  envInt("PREVIEW_FETCH_TIMEOUT_SEC", 5),
		SharedSecret:       os.Getenv("SHARED_SECRET"),
		CORSOrigins:        splitCSV(envOr("CORS_ORIGINS", "*")),
		MinIO: MinIOConfig{
			Endpoint:  envOr("MINIO_ENDPOINT", "localhost:9000"),
			AccessKey: envOr("MINIO_ACCESS_KEY", "minioadmin"),
			SecretKey: envOr("MINIO_SECRET_KEY", "minioadmin"),
			Bucket:    envOr("MINIO_BUCKET", "foldex-screenshots"),
			UseSSL:    envBool("MINIO_USE_SSL", false),
		},
	}
	if cfg.DBURL == "" {
		return cfg, errors.New("DB_URL is required")
	}
	if cfg.PreviewConcurrency < 1 {
		cfg.PreviewConcurrency = 1
	}
	return cfg, nil
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

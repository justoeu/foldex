package keyfile

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestS3776Charter_Load(t *testing.T) {
	t.Parallel()
	logger := quietLogger()

	t.Run("env value wins and is validated", func(t *testing.T) {
		t.Parallel()
		want := bytes.Repeat([]byte{0xAB}, 32)
		cfg := baseConfig()
		cfg.EnvValue = base64.StdEncoding.EncodeToString(want)
		got, err := Load(cfg, logger)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatal("env value was not returned")
		}

		cfg.EnvValue = "!!!!"
		_, err = Load(cfg, logger)
		if err == nil || !strings.Contains(err.Error(), "not valid base64") {
			t.Fatalf("want base64 error, got %v", err)
		}
		cfg.EnvValue = base64.StdEncoding.EncodeToString(make([]byte, 31))
		_, err = Load(cfg, logger)
		if err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
			t.Fatalf("want length error, got %v", err)
		}
	})

	t.Run("without autogenerate", func(t *testing.T) {
		t.Parallel()
		cfg := baseConfig()
		cfg.Path = filepath.Join(t.TempDir(), "missing")
		_, err := Load(cfg, logger)
		if err == nil || !strings.Contains(err.Error(), "TEST_KEY") {
			t.Fatalf("want env-var error, got %v", err)
		}
	})

	t.Run("ephemeral allowed vs refused", func(t *testing.T) {
		t.Parallel()
		ok := baseConfig()
		ok.AutoGenerate = true
		ok.AllowEphemeral = true
		key, err := Load(ok, logger)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(key) != MinKeyBytes {
			t.Fatalf("key length = %d", len(key))
		}

		refused := baseConfig()
		refused.AutoGenerate = true
		refused.AllowEphemeral = false
		_, err = Load(refused, logger)
		if err == nil || !strings.Contains(err.Error(), "TEST_KEY_PATH") {
			t.Fatalf("want path-var error, got %v", err)
		}
	})

	t.Run("invalid persisted key is not replaced", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "key")
		original := []byte("not-base64")
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := baseConfig()
		cfg.Path = path
		cfg.AutoGenerate = true
		if _, err := Load(cfg, logger); err == nil {
			t.Fatal("invalid persisted key was replaced")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, original) {
			t.Fatal("persisted key changed")
		}
	})
}

package keyfile

import (
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseConfig() Config {
	return Config{
		Name:    "test key",
		EnvVar:  "TEST_KEY",
		PathVar: "TEST_KEY_PATH",
	}
}

func TestEnvValueWinsOverEverything(t *testing.T) {
	t.Parallel()
	want := bytes.Repeat([]byte{0xAB}, 32)
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	// A DIFFERENT key already on disk — the env value must still win, so an
	// operator can override a stale state file without deleting it.
	if err := Write(path, bytes.Repeat([]byte{0x11}, 32)); err != nil {
		t.Fatal(err)
	}

	cfg := baseConfig()
	cfg.EnvValue = base64.StdEncoding.EncodeToString(want)
	cfg.Path = path

	got, err := Load(cfg, quietLogger())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("env value did not take precedence over the state file")
	}
}

func TestEnvValueIsValidated(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, value, wantIn string
	}{
		{"not base64", "!!!!not base64!!!!", "not valid base64"},
		{"too short", base64.StdEncoding.EncodeToString(make([]byte, 31)), "at least 32 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseConfig()
			cfg.EnvValue = tc.value
			_, err := Load(cfg, quietLogger())
			if err == nil {
				t.Fatal("want error")
			}
			// The error must name the env var so the operator knows the knob.
			if !strings.Contains(err.Error(), "TEST_KEY") || !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("unhelpful error: %v", err)
			}
		})
	}
}

func TestPersistedKeyIsReusedAcrossLoads(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "key")
	cfg := baseConfig()
	cfg.Path = path
	cfg.AutoGenerate = true

	first, err := Load(cfg, quietLogger())
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	second, err := Load(cfg, quietLogger())
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	// Stability is the whole point for an at-rest encryption key: a second boot
	// that mints a fresh key would make every stored ciphertext unopenable.
	if !bytes.Equal(first, second) {
		t.Fatal("a second Load generated a different key instead of reading the persisted one")
	}
}

// The persisted key must not be world- or group-readable. It is as sensitive as
// the VAPID private key, and the process umask cannot be trusted to be strict.
func TestPersistedKeyIs0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "key")
	cfg := baseConfig()
	cfg.Path = path
	cfg.AutoGenerate = true

	if _, err := Load(cfg, quietLogger()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file mode = %o, want 600", perm)
	}
}

func TestWithoutAutoGenerateLoadFails(t *testing.T) {
	t.Parallel()
	cfg := baseConfig()
	cfg.Path = filepath.Join(t.TempDir(), "missing")

	_, err := Load(cfg, quietLogger())
	if err == nil || !strings.Contains(err.Error(), "TEST_KEY") {
		t.Fatalf("want an actionable error naming the env var, got %v", err)
	}
}

// The AllowEphemeral split is the reason this package exists. A key whose loss
// destroys data at rest must refuse to run session-only; one whose loss is
// merely inconvenient may proceed with a warning.
func TestEphemeralPolicy(t *testing.T) {
	t.Parallel()

	t.Run("allowed: no path yields a session-only key", func(t *testing.T) {
		t.Parallel()
		cfg := baseConfig()
		cfg.AutoGenerate = true
		cfg.AllowEphemeral = true

		key, err := Load(cfg, quietLogger())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(key) != MinKeyBytes {
			t.Fatalf("key length = %d, want %d", len(key), MinKeyBytes)
		}
	})

	t.Run("refused: no path is a boot failure", func(t *testing.T) {
		t.Parallel()
		cfg := baseConfig()
		cfg.AutoGenerate = true
		cfg.AllowEphemeral = false

		_, err := Load(cfg, quietLogger())
		if err == nil {
			t.Fatal("a non-ephemeral key must not start session-only")
		}
		// Name the path variable: the fix is to set it, not the env key.
		if !strings.Contains(err.Error(), "TEST_KEY_PATH") {
			t.Fatalf("error should point at the path variable, got %v", err)
		}
	})

	t.Run("refused: an unwritable path is a boot failure", func(t *testing.T) {
		t.Parallel()
		// A regular file used as a parent directory makes MkdirAll fail.
		blocker := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := baseConfig()
		cfg.Path = filepath.Join(blocker, "key")
		cfg.AutoGenerate = true
		cfg.AllowEphemeral = false

		if _, err := Load(cfg, quietLogger()); err == nil {
			t.Fatal("persist failure must abort boot when the key cannot be ephemeral")
		}
	})

	t.Run("allowed: an unwritable path only warns", func(t *testing.T) {
		t.Parallel()
		blocker := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := baseConfig()
		cfg.Path = filepath.Join(blocker, "key")
		cfg.AutoGenerate = true
		cfg.AllowEphemeral = true

		if _, err := Load(cfg, quietLogger()); err != nil {
			t.Fatalf("an ephemeral-tolerant key should survive a persist failure: %v", err)
		}
	})
}

func TestReadRejectsUnusableStateFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte(base64.StdEncoding.EncodeToString(make([]byte, 8))), 0o600); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("!!!"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"missing":    filepath.Join(dir, "nope"),
		"short key":  short,
		"not base64": garbage,
		"empty path": "",
		"dot path":   ".",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Read(path); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestWriteRejectsEmptyPath(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"", "."} {
		if err := Write(p, make([]byte, 32)); err == nil {
			t.Fatalf("Write(%q) should fail", p)
		}
	}
}

// A state file whose content has trailing whitespace (an operator echoing a key
// into it) must still load — otherwise the failure mode is a mysterious boot
// error on an entirely reasonable file.
func TestReadToleratesTrailingNewline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "key")
	want := bytes.Repeat([]byte{0x7F}, 32)
	body := base64.StdEncoding.EncodeToString(want) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("key mismatch")
	}
}

package push

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLoadOrGenerate_FromEnv(t *testing.T) {
	keys, err := LoadOrGenerate("pub123", "priv456", "mailto:me@host", "", false, testLogger())
	require.NoError(t, err)
	assert.Equal(t, "pub123", keys.PublicKey)
	assert.Equal(t, "priv456", keys.PrivateKey)
	assert.Equal(t, "mailto:me@host", keys.Subject)
}

func TestLoadOrGenerate_PartialEnvErrors(t *testing.T) {
	_, err := LoadOrGenerate("pub-only", "", "", "", true, testLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VAPID config incomplete")
}

func TestLoadOrGenerate_SubjectDefault(t *testing.T) {
	keys, err := LoadOrGenerate("p", "k", "", "", false, testLogger())
	require.NoError(t, err)
	assert.Equal(t, "mailto:foldex@localhost", keys.Subject)
}

func TestLoadOrGenerate_NoEnvNoAutogenErrors(t *testing.T) {
	_, err := LoadOrGenerate("", "", "", "", false, testLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VAPID keys not configured")
}

func TestLoadOrGenerate_AutogenAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vapid.json")

	keys, err := LoadOrGenerate("", "", "", path, true, testLogger())
	require.NoError(t, err)
	assert.NotEmpty(t, keys.PublicKey)
	assert.NotEmpty(t, keys.PrivateKey)

	// State file must exist with 0600 mode.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// File contents are JSON with both keys.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var s stateFile
	require.NoError(t, json.Unmarshal(raw, &s))
	assert.Equal(t, keys.PublicKey, s.PublicKey)
	assert.Equal(t, keys.PrivateKey, s.PrivateKey)
}

func TestLoadOrGenerate_ReadsExistingState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vapid.json")

	// Seed the file.
	require.NoError(t, writeState(path, VAPIDKeys{
		PublicKey: "seeded-pub", PrivateKey: "seeded-priv", Subject: "mailto:seed@h",
	}))

	keys, err := LoadOrGenerate("", "", "", path, true, testLogger())
	require.NoError(t, err)
	assert.Equal(t, "seeded-pub", keys.PublicKey)
	assert.Equal(t, "seeded-priv", keys.PrivateKey)
}

func TestLoadOrGenerate_EnvOverridesStateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vapid.json")
	require.NoError(t, writeState(path, VAPIDKeys{
		PublicKey: "old", PrivateKey: "old", Subject: "x",
	}))

	keys, err := LoadOrGenerate("env-pub", "env-priv", "", path, true, testLogger())
	require.NoError(t, err)
	assert.Equal(t, "env-pub", keys.PublicKey, "explicit env must win over persisted state")
}

func TestLoadOrGenerate_SessionOnlyWhenNoStatePath(t *testing.T) {
	keys, err := LoadOrGenerate("", "", "", "", true, testLogger())
	require.NoError(t, err)
	assert.NotEmpty(t, keys.PublicKey)
}

func TestLoadOrGenerate_SubjectOverrideOnState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vapid.json")
	require.NoError(t, writeState(path, VAPIDKeys{
		PublicKey: "p", PrivateKey: "k", Subject: "mailto:old@h",
	}))
	keys, err := LoadOrGenerate("", "", "mailto:new@h", path, true, testLogger())
	require.NoError(t, err)
	assert.Equal(t, "mailto:new@h", keys.Subject)
}

func TestReadState_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(path, []byte(`{not json`), 0o600))
	_, err := readState(path)
	require.Error(t, err)
}

func TestReadState_MissingKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"public_key":"","private_key":""}`), 0o600))
	_, err := readState(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing keys")
}

func TestLoadOrGenerate_PersistFailureIsNonFatal(t *testing.T) {
	// Point state path at a file path whose parent cannot be created as a dir
	// (parent is a file) — writeState fails, but LoadOrGenerate still returns keys.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	path := filepath.Join(blocker, "vapid.json") // parent is a file → MkdirAll fails
	keys, err := LoadOrGenerate("", "", "", path, true, testLogger())
	require.NoError(t, err)
	assert.NotEmpty(t, keys.PublicKey)
}

func TestDefaultSubject(t *testing.T) {
	assert.Equal(t, "mailto:foldex@localhost", defaultSubject(""))
	assert.Equal(t, "mailto:me@x", defaultSubject("mailto:me@x"))
}

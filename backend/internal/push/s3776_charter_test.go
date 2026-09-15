package push

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3776Charter_LoadOrGenerate(t *testing.T) {
	t.Parallel()
	logger := testLogger()

	t.Run("explicit env wins", func(t *testing.T) {
		t.Parallel()
		got, err := LoadOrGenerate("pub123", "priv456", "mailto:me@host", "", false, logger)
		require.NoError(t, err)
		assert.Equal(t, "pub123", got.PublicKey)
		assert.Equal(t, "priv456", got.PrivateKey)
		assert.Equal(t, "mailto:me@host", got.Subject)
	})

	t.Run("partial env is a config bug", func(t *testing.T) {
		t.Parallel()
		_, err := LoadOrGenerate("pub-only", "", "", "", true, logger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VAPID config incomplete")
	})

	t.Run("missing env without autogen", func(t *testing.T) {
		t.Parallel()
		_, err := LoadOrGenerate("", "", "", "", false, logger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VAPID keys not configured")
	})

	t.Run("autogen persists and reuses", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "vapid.json")
		first, err := LoadOrGenerate("", "", "", path, true, logger)
		require.NoError(t, err)
		require.NotEmpty(t, first.PublicKey)
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		second, err := LoadOrGenerate("", "", "", path, true, logger)
		require.NoError(t, err)
		assert.Equal(t, first.PublicKey, second.PublicKey)
		assert.Equal(t, first.PrivateKey, second.PrivateKey)
	})

	t.Run("persist failure is non-fatal", func(t *testing.T) {
		t.Parallel()
		blocker := filepath.Join(t.TempDir(), "blocker")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
		path := filepath.Join(blocker, "vapid.json")
		got, err := LoadOrGenerate("", "", "", path, true, logger)
		require.NoError(t, err)
		assert.NotEmpty(t, got.PublicKey)
	})

	t.Run("subject default", func(t *testing.T) {
		t.Parallel()
		got, err := LoadOrGenerate("p", "k", "", "", false, logger)
		require.NoError(t, err)
		assert.Equal(t, "mailto:foldex@localhost", got.Subject)
		var s stateFile
		require.NoError(t, json.Unmarshal([]byte(`{"public_key":"a","private_key":"b"}`), &s))
		assert.Equal(t, "a", s.PublicKey)
	})
}

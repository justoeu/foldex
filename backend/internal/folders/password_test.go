package folders

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/httperr"
)

func TestHashPassword_VerifyPassword_RoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)
	assert.NotEqual(t, "correct horse battery staple", hash, "hash must not equal the plaintext")
	assert.True(t, VerifyPassword(hash, "correct horse battery staple"))
	assert.False(t, VerifyPassword(hash, "wrong password"))
}

func TestIssueUnlockToken_VerifyUnlockToken_HappyPath(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	hash, err := HashPassword("secret123")
	require.NoError(t, err)

	token := IssueUnlockToken(secret, 42, hash)
	assert.True(t, VerifyUnlockToken(secret, 42, hash, token))
}

func TestVerifyUnlockToken_RejectsWrongFolderOrSecret(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	hash, err := HashPassword("secret123")
	require.NoError(t, err)
	token := IssueUnlockToken(secret, 42, hash)

	assert.False(t, VerifyUnlockToken(secret, 43, hash, token), "token minted for a different folder id must not verify")
	assert.False(t, VerifyUnlockToken([]byte("different-secret-32-bytes-long!"), 42, hash, token), "token minted with a different secret must not verify")
	assert.False(t, VerifyUnlockToken(secret, 42, hash, "garbage"), "malformed token must not verify")
	assert.False(t, VerifyUnlockToken(secret, 42, hash, ""), "empty token must not verify")
}

func TestVerifyUnlockToken_InvalidatedByPasswordChange(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	oldHash, err := HashPassword("old-password")
	require.NoError(t, err)
	newHash, err := HashPassword("new-password")
	require.NoError(t, err)

	token := IssueUnlockToken(secret, 7, oldHash)
	assert.True(t, VerifyUnlockToken(secret, 7, oldHash, token))
	// Recomputing against the CURRENT (new) hash is exactly what the
	// repository does on every gated request — this is the free
	// invalidation-on-password-change property the design relies on.
	assert.False(t, VerifyUnlockToken(secret, 7, newHash, token), "token issued against the old password hash must not verify against the new one")
}

func TestVerifyUnlockToken_RejectsExpiredToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	hash, err := HashPassword("secret123")
	require.NoError(t, err)

	// Hand-craft an already-expired token (exp = 1 second ago) instead of
	// sleeping past the real 24h TTL.
	exp := time.Now().Add(-1 * time.Second).Unix()
	mac := signUnlockToken(secret, 42, hash, exp)
	expired := mac + "." + strconv.FormatInt(exp, 10)

	assert.False(t, VerifyUnlockToken(secret, 42, hash, expired))
}

func TestCheckUnlock(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	hash, err := HashPassword("secret123")
	require.NoError(t, err)
	token := IssueUnlockToken(secret, 1, hash)

	assert.NoError(t, CheckUnlock(secret, 1, nil, ""), "unprotected folder never requires a token")
	assert.NoError(t, CheckUnlock(secret, 1, &hash, token), "valid token for the right folder must pass")

	err = CheckUnlock(secret, 1, &hash, "")
	require.Error(t, err)
	assertFolderLocked(t, err)

	err = CheckUnlock(secret, 1, &hash, "bogus-token")
	require.Error(t, err)
	assertFolderLocked(t, err)
}

func assertFolderLocked(t *testing.T, err error) {
	t.Helper()
	var he *httperr.Error
	require.True(t, errors.As(err, &he))
	assert.Equal(t, http.StatusForbidden, he.Status)
	assert.Equal(t, "folder_locked", he.Code)
}

func TestLoadOrGenerateFolderUnlockKey_EnvOverride(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)

	key, err := LoadOrGenerateFolderUnlockKey(encoded, "", true, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, raw, key)
}

func TestLoadOrGenerateFolderUnlockKey_RejectsShortEnvKey(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("too-short"))
	_, err := LoadOrGenerateFolderUnlockKey(short, "", true, slog.Default())
	require.Error(t, err)
}

func TestLoadOrGenerateFolderUnlockKey_GenerateAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "folder_unlock.key")

	key1, err := LoadOrGenerateFolderUnlockKey("", path, true, slog.Default())
	require.NoError(t, err)
	assert.Len(t, key1, 32)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Second call must load the SAME persisted key, not generate a new one —
	// otherwise every restart would invalidate all outstanding unlock
	// tokens even without a password change.
	key2, err := LoadOrGenerateFolderUnlockKey("", path, true, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, key1, key2)
}

func TestLoadOrGenerateFolderUnlockKey_RefusesWithoutAutoGenOrState(t *testing.T) {
	_, err := LoadOrGenerateFolderUnlockKey("", "", false, slog.Default())
	require.Error(t, err)
}

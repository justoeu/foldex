package folders

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"foldex/internal/pkg/keyfile"
	"foldex/internal/pkg/pwhash"
)

// HashPassword bcrypt-hashes a plaintext folder password for storage in
// folder.password_hash. Never store or log the plaintext. Thin alias over the
// shared pwhash leaf so folder and master passwords use identical hashing.
func HashPassword(plain string) (string, error) { return pwhash.Hash(plain) }

// VerifyPassword reports whether plain matches the bcrypt hash.
func VerifyPassword(hash, plain string) bool { return pwhash.Verify(hash, plain) }

// unlockTokenTTL is a safety ceiling, not the intended session length — the
// frontend never persists the token past a page reload (CLAUDE.md-documented
// decision: unlock state is session-only), so this just bounds how long a
// token could theoretically be replayed if it leaked.
const unlockTokenTTL = 24 * time.Hour

// UnlockHeader carries a folder unlock token on requests that read a
// protected folder's contents — checked by the parent_id-scoped folder list
// and the folder_id-scoped links, notes, and entries lists.
const UnlockHeader = "X-Foldex-Folder-Unlock"

// IssueUnlockToken mints a token proving the caller supplied the correct
// password for folderID at issuance time. The HMAC input includes the
// folder's CURRENT password_hash, so changing or clearing the password
// invalidates every previously issued token automatically — no separate
// revocation list needed.
func IssueUnlockToken(secret []byte, folderID int64, passwordHash string) string {
	exp := time.Now().Add(unlockTokenTTL).Unix()
	return signUnlockToken(secret, folderID, passwordHash, exp) + "." + strconv.FormatInt(exp, 10)
}

// VerifyUnlockToken checks a token against the folder's CURRENT password
// hash (fetched fresh from the DB by the caller, never trusted from the
// token itself) and expiry.
func VerifyUnlockToken(secret []byte, folderID int64, passwordHash, token string) bool {
	mac, expStr, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	expected := signUnlockToken(secret, folderID, passwordHash, exp)
	return hmac.Equal([]byte(mac), []byte(expected))
}

func signUnlockToken(secret []byte, folderID int64, passwordHash string, exp int64) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(fmt.Sprintf("%d:%d:%s", folderID, exp, passwordHash)))
	return hex.EncodeToString(mac.Sum(nil))
}

// CheckUnlock enforces the content-gate for a single folder: nil (allowed)
// when the folder is unprotected (passwordHash == nil), or when token
// verifies against the folder's current password hash; otherwise ErrLocked.
// Redaction of preview_links/preview_folders (the
// OTHER half of the protection story) happens unconditionally in the
// repository layer regardless of token presence — this function gates the
// folder-scoped list endpoints.
func CheckUnlock(secret []byte, folderID int64, passwordHash *string, token string) error {
	if passwordHash == nil {
		return nil
	}
	if token == "" || !VerifyUnlockToken(secret, folderID, *passwordHash, token) {
		return ErrLocked
	}
	return nil
}

// LoadOrGenerateFolderUnlockKey resolves the folder-unlock-token HMAC secret.
//
// The resolution policy (env → state file → autogenerate at 0600) lives in
// internal/pkg/keyfile, shared with the TOTP seed-encryption key. Ephemeral is
// ALLOWED here: losing this key only invalidates outstanding unlock tokens, and
// a user simply re-enters the folder password — which the session-only unlock
// model already asks of them on every reload.
func LoadOrGenerateFolderUnlockKey(envKeyB64, statePath string, autoGen bool, logger *slog.Logger) ([]byte, error) {
	return keyfile.Load(keyfile.Config{
		Name:           "folder unlock key",
		EnvVar:         "FOLDER_UNLOCK_KEY",
		PathVar:        "FOLDER_UNLOCK_KEY_PATH",
		EnvValue:       envKeyB64,
		Path:           statePath,
		AutoGenerate:   autoGen,
		AllowEphemeral: true,
	}, logger)
}

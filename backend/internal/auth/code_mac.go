package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"

	"foldex/internal/pkg/authctx"
)

const codeMACVersion byte = 1

const (
	emailOTPSubkeyPurpose     = "foldex/auth/code-mac/subkey/v1/email-otp"
	recoverySubkeyPurpose     = "foldex/auth/code-mac/subkey/v1/recovery-code"
	recoveryCredentialPurpose = "recovery_code"
)

// CodeMAC creates indexed, non-enumerable digests for low-entropy auth codes.
// Its keys are derived from AUTH_ENCRYPTION_KEY, never reused from AES directly.
type CodeMAC struct {
	emailOTPKey [sha256.Size]byte
	recoveryKey [sha256.Size]byte
}

// NewCodeMAC derives purpose-separated HMAC-SHA256 subkeys from the key already
// loaded for auth encryption. The derived values, not the AES key, key code MACs.
func NewCodeMAC(masterKey []byte) (*CodeMAC, error) {
	if len(masterKey) < 32 {
		return nil, fmt.Errorf("auth: code MAC key must be at least 32 bytes, got %d", len(masterKey))
	}
	return &CodeMAC{
		emailOTPKey: deriveCodeMACSubkey(masterKey, emailOTPSubkeyPurpose),
		recoveryKey: deriveCodeMACSubkey(masterKey, recoverySubkeyPurpose),
	}, nil
}

func deriveCodeMACSubkey(masterKey []byte, purpose string) [sha256.Size]byte {
	m := hmac.New(sha256.New, masterKey)
	writeMACString(m, purpose)
	var key [sha256.Size]byte
	copy(key[:], m.Sum(nil))
	return key
}

// EmailOTPDigest binds a mailed login code to its protocol version, purpose,
// owner and exact challenge. A database snapshot has none of the HMAC key.
func (m *CodeMAC) EmailOTPDigest(uid authctx.UserID, purpose string, challengeID *int64, code string) []byte {
	return m.emailOTPDigest(codeMACVersion, uid, purpose, challengeID, code)
}

func (m *CodeMAC) emailOTPDigest(version byte, uid authctx.UserID, purpose string, challengeID *int64, code string) []byte {
	mac := hmac.New(sha256.New, m.emailOTPKey[:])
	writeMACHeader(mac, version, purpose, uid)
	if challengeID == nil {
		_, _ = mac.Write([]byte{0})
	} else {
		_, _ = mac.Write([]byte{1})
		writeMACUint64(mac, uint64(*challengeID)) // #nosec G115 -- fixed-width MAC serialization; the int64→uint64 mapping is bijective, not arithmetic
	}
	writeMACString(mac, code)
	return mac.Sum(nil)
}

// RecoveryCodeDigest uses a separate subkey and domain from e-mail OTPs and
// binds every code to its owner before it reaches the indexed lookup.
func (m *CodeMAC) RecoveryCodeDigest(uid authctx.UserID, code string) []byte {
	return m.recoveryCodeDigest(codeMACVersion, uid, code)
}

func (m *CodeMAC) recoveryCodeDigest(version byte, uid authctx.UserID, code string) []byte {
	mac := hmac.New(sha256.New, m.recoveryKey[:])
	writeMACHeader(mac, version, recoveryCredentialPurpose, uid)
	writeMACString(mac, code)
	return mac.Sum(nil)
}

func writeMACHeader(mac hash.Hash, version byte, purpose string, uid authctx.UserID) {
	_, _ = mac.Write([]byte{version})
	writeMACString(mac, purpose)
	writeMACUint64(mac, uint64(uid)) // #nosec G115 -- fixed-width MAC serialization; the int64→uint64 mapping is bijective, not arithmetic
}

func writeMACString(mac hash.Hash, value string) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value))) // #nosec G115 -- length prefix for short purposes/codes, nowhere near 4 GiB
	_, _ = mac.Write(size[:])
	_, _ = mac.Write([]byte(value))
}

func writeMACUint64(mac hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = mac.Write(encoded[:])
}

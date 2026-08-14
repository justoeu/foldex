package auth

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func TestEmailOTPMACBindsKeyVersionPurposeUserChallengeAndCode(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x31}, 32)
	otherKey := bytes.Repeat([]byte{0x32}, 32)
	mac, err := NewCodeMAC(key)
	require.NoError(t, err)
	other, err := NewCodeMAC(otherKey)
	require.NoError(t, err)

	uid := authctx.UserID(41)
	challenge := int64(73)
	digest := mac.EmailOTPDigest(uid, OTPPurposeLogin2FA, &challenge, "123456")
	rawSHA := sha256.Sum256([]byte("123456"))
	assert.NotEqual(t, rawSHA[:], digest)
	assert.Equal(t, digest, mac.EmailOTPDigest(uid, OTPPurposeLogin2FA, &challenge, "123456"))
	assert.NotEqual(t, digest, other.EmailOTPDigest(uid, OTPPurposeLogin2FA, &challenge, "123456"))
	assert.NotEqual(t, digest, mac.emailOTPDigest(codeMACVersion+1, uid, OTPPurposeLogin2FA, &challenge, "123456"))
	assert.NotEqual(t, digest, mac.EmailOTPDigest(uid, OTPPurposeVerifyEmail, &challenge, "123456"))
	assert.NotEqual(t, digest, mac.EmailOTPDigest(uid+1, OTPPurposeLogin2FA, &challenge, "123456"))
	otherChallenge := challenge + 1
	assert.NotEqual(t, digest, mac.EmailOTPDigest(uid, OTPPurposeLogin2FA, &otherChallenge, "123456"))
	assert.NotEqual(t, digest, mac.EmailOTPDigest(uid, OTPPurposeLogin2FA, nil, "123456"))
	assert.NotEqual(t, digest, mac.EmailOTPDigest(uid, OTPPurposeLogin2FA, &challenge, "123457"))
}

func TestRecoveryCodeMACUsesDerivedSeparateDomainAndUserBinding(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x51}, 32)
	mac, err := NewCodeMAC(key)
	require.NoError(t, err)
	uid := authctx.UserID(19)
	code := "1A2B3C4D5E6F7G8H"
	digest := mac.RecoveryCodeDigest(uid, code)
	rawSHA := sha256.Sum256([]byte(code))

	assert.NotEqual(t, rawSHA[:], digest)
	assert.NotEqual(t, key, mac.recoveryKey[:], "the AES key was reused directly for HMAC")
	assert.NotEqual(t, mac.emailOTPKey, mac.recoveryKey, "e-mail and recovery share a subkey")
	assert.NotEqual(t, digest, mac.RecoveryCodeDigest(uid+1, code))
	assert.NotEqual(t, digest, mac.RecoveryCodeDigest(uid, code+"J"))
	assert.NotEqual(t, digest, mac.recoveryCodeDigest(codeMACVersion+1, uid, code))
	assert.NotEqual(t, digest, mac.EmailOTPDigest(uid, recoveryCredentialPurpose, nil, code),
		"the same input was interchangeable between recovery and e-mail domains")
}

func TestNewCodeMACRejectsShortMasterKey(t *testing.T) {
	t.Parallel()
	_, err := NewCodeMAC(make([]byte, 31))
	require.Error(t, err)
}

package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Cipher is authenticated symmetric encryption for secrets that must be
// RECOVERABLE, not merely verifiable.
//
// Everything else in this package is one-way on purpose: session tokens are
// compared by hash, while low-entropy recovery codes and OTPs use keyed MACs.
// The server never needs the original back. A TOTP seed is the exception:
// validating a code requires the seed itself, so hashing is not an option.
// Encryption is what remains, and
// AES-256-GCM is chosen for the authentication tag: without it, a database with
// write access becomes a seed-substitution attack, since a silently corrupted
// seed is indistinguishable from a user whose authenticator drifted.
type Cipher struct {
	aead cipher.AEAD
}

// ErrDecrypt is returned for any failure to open a ciphertext: wrong key,
// tampered bytes, truncated nonce.
//
// One error for all of them, deliberately. The distinction between "this key is
// wrong" and "these bytes were altered" is of no use to a caller and every use
// to an attacker probing which of the two they achieved.
var ErrDecrypt = errors.New("secrets: cannot decrypt")

// NewCipher builds a Cipher from a 32-byte key (AES-256).
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("secrets: key must be at least 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return nil, fmt.Errorf("secrets: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// NewDerivedCipher builds a Cipher for ONE purpose from a master key, so two
// unrelated domains never share the same AES key.
//
// The TOTP seed came first and uses AUTH_ENCRYPTION_KEY directly; anything
// added afterwards derives instead, for the same reason the code MACs already
// derive per-purpose subkeys rather than reusing the AES key. Two domains under
// one key share a (key, nonce) space, so their safety margins add up instead of
// being independent — and rotating one becomes impossible without destroying
// the other. That matters most where the volumes differ by orders of magnitude:
// TOTP encrypts once per user, while the mail outbox encrypts once per reset
// link, sign-in code and invitation.
//
// The purpose string is a domain separator, so it must be a compile-time
// constant that never changes: a new value is a new key, and every ciphertext
// written under the old one becomes undecryptable.
func NewDerivedCipher(masterKey []byte, purpose string) (*Cipher, error) {
	if len(masterKey) < 32 {
		return nil, fmt.Errorf("secrets: master key must be at least 32 bytes, got %d", len(masterKey))
	}
	m := hmac.New(sha256.New, masterKey)
	_, _ = m.Write([]byte(purpose))
	return NewCipher(m.Sum(nil))
}

// Encrypt returns the ciphertext and the nonce that produced it, stored in
// separate columns.
//
// The nonce is returned rather than prefixed onto the ciphertext because the
// schema already has a column for it (totp_secret.secret_nonce), and a caller
// that has to split a blob at the right offset is a caller that can get the
// offset wrong.
func (c *Cipher) Encrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, c.aead.NonceSize())
	// A repeated nonce under the same key is catastrophic for GCM — it leaks
	// the XOR of the two plaintexts and, worse, the authentication subkey. A
	// random 96-bit nonce is safe here because every domain gets its OWN key:
	// TOTP seeds are bounded by the number of users, and the mail outbox — the
	// one domain whose volume is not bounded by anything — encrypts under a
	// key of its own via NewDerivedCipher. Each therefore sits on its own
	// distance from the 2^48-message birthday bound instead of sharing one.
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	return c.aead.Seal(nil, nonce, plaintext, nil), nonce, nil
}

// Decrypt reverses Encrypt, returning ErrDecrypt for any failure.
func (c *Cipher) Decrypt(ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != c.aead.NonceSize() {
		return nil, ErrDecrypt
	}
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

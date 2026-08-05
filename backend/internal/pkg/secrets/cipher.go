package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// Cipher is authenticated symmetric encryption for secrets that must be
// RECOVERABLE, not merely verifiable.
//
// Everything else in this package is one-way on purpose: session tokens,
// recovery codes and OTPs are compared by hash, so the server never needs the
// original back. A TOTP seed is the exception — validating a code requires the
// seed itself, so hashing is not an option. Encryption is what remains, and
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
	// random 96-bit nonce is safe here because the number of seeds encrypted
	// per key is bounded by the number of users, nowhere near the birthday
	// bound of 2^48 messages.
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

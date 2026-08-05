package secrets

import (
	"bytes"
	"errors"
	"testing"
)

func testKey(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b + byte(i)
	}
	return k
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()
	c, err := NewCipher(testKey(1))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	seed := []byte("JBSWY3DPEHPK3PXP")

	ct, nonce, err := c.Encrypt(seed)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(ct, seed) {
		t.Fatal("ciphertext contains the plaintext seed")
	}
	got, err := c.Decrypt(ct, nonce)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, seed) {
		t.Fatalf("round trip = %q, want %q", got, seed)
	}
}

// Two encryptions of the SAME seed must differ. If they did not, the column
// would leak which users share a seed — and, since the seed is generated per
// user, equal ciphertexts would mean a nonce was reused, which breaks GCM
// outright.
func TestEncryptIsNonDeterministic(t *testing.T) {
	t.Parallel()
	c, _ := NewCipher(testKey(2))
	seed := []byte("JBSWY3DPEHPK3PXP")

	ct1, n1, _ := c.Encrypt(seed)
	ct2, n2, _ := c.Encrypt(seed)
	if bytes.Equal(n1, n2) {
		t.Fatal("nonce repeated across encryptions — GCM is broken under nonce reuse")
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatal("identical ciphertext for identical plaintext")
	}
}

// The authentication tag is the reason this is AES-GCM and not AES-CTR: an
// attacker with write access to the database must not be able to swap a seed
// for one they control, because the victim would see only "my authenticator
// stopped working".
func TestTamperedCiphertextIsRejected(t *testing.T) {
	t.Parallel()
	c, _ := NewCipher(testKey(3))
	ct, nonce, _ := c.Encrypt([]byte("JBSWY3DPEHPK3PXP"))

	for _, tc := range []struct {
		name      string
		ct, nonce []byte
	}{
		{"flipped ciphertext bit", flip(ct, 0), nonce},
		{"flipped tag bit", flip(ct, len(ct)-1), nonce},
		{"wrong nonce", ct, flip(nonce, 0)},
		{"truncated nonce", ct, nonce[:len(nonce)-1]},
		{"empty nonce", ct, nil},
		{"truncated ciphertext", ct[:len(ct)-1], nonce},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Decrypt(tc.ct, tc.nonce); !errors.Is(err, ErrDecrypt) {
				t.Fatalf("want ErrDecrypt, got %v", err)
			}
		})
	}
}

// A ciphertext must not open under a different key. This is what makes losing
// AUTH_ENCRYPTION_KEY unrecoverable — and therefore why keyfile refuses to
// silently regenerate it.
func TestWrongKeyCannotDecrypt(t *testing.T) {
	t.Parallel()
	a, _ := NewCipher(testKey(4))
	b, _ := NewCipher(testKey(9))
	ct, nonce, _ := a.Encrypt([]byte("JBSWY3DPEHPK3PXP"))

	if _, err := b.Decrypt(ct, nonce); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("a different key decrypted the seed: %v", err)
	}
}

func TestNewCipherRejectsShortKey(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 16, 31} {
		if _, err := NewCipher(make([]byte, n)); err == nil {
			t.Fatalf("%d-byte key accepted; AES-256 needs 32", n)
		}
	}
	if _, err := NewCipher(make([]byte, 64)); err != nil {
		t.Fatalf("64-byte key should be accepted (first 32 used): %v", err)
	}
}

func flip(b []byte, i int) []byte {
	out := append([]byte(nil), b...)
	out[i] ^= 0x01
	return out
}

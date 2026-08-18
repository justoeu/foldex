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

// Domain separation is the whole point: a ciphertext written by one purpose's
// cipher must not open under another's, or deriving would be decoration.
func TestDerivedCiphersAreDomainSeparated(t *testing.T) {
	t.Parallel()
	master := testKey(7)

	a, err := NewDerivedCipher(master, "foldex/purpose-a/v1")
	if err != nil {
		t.Fatalf("NewDerivedCipher: %v", err)
	}
	b, err := NewDerivedCipher(master, "foldex/purpose-b/v1")
	if err != nil {
		t.Fatalf("NewDerivedCipher: %v", err)
	}
	ct, nonce, err := a.Encrypt([]byte("https://foldex.test/#reset=TOKEN"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := b.Decrypt(ct, nonce); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("a sibling purpose opened the ciphertext: %v", err)
	}

	// The master key itself must not open it either — that is the property the
	// derivation exists to create, since the TOTP seed still uses the master.
	direct, err := NewCipher(master)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	if _, err := direct.Decrypt(ct, nonce); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("the master key opened a derived ciphertext: %v", err)
	}
}

// Derivation must be deterministic, or a restart would strand every row the
// previous process wrote.
func TestDerivedCipherIsStableAcrossCalls(t *testing.T) {
	t.Parallel()
	master := testKey(8)
	a, _ := NewDerivedCipher(master, "foldex/stable/v1")
	b, _ := NewDerivedCipher(master, "foldex/stable/v1")

	ct, nonce, err := a.Encrypt([]byte("code 123456"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := b.Decrypt(ct, nonce)
	if err != nil {
		t.Fatalf("a second derivation of the same purpose could not decrypt: %v", err)
	}
	if string(got) != "code 123456" {
		t.Fatalf("round trip = %q", got)
	}
}

func TestNewDerivedCipherRejectsAShortMasterKey(t *testing.T) {
	t.Parallel()
	if _, err := NewDerivedCipher(make([]byte, 31), "p"); err == nil {
		t.Fatal("a 31-byte master key was accepted")
	}
}

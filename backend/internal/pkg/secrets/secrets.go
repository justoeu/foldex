// Package secrets mints and verifies the opaque credentials the auth stack
// hands out: session tokens, CSRF tokens, invite tokens, password-reset tokens.
//
// It is a leaf (stdlib only), like authctx and httperr, so every auth-adjacent
// package can depend on it without an import cycle.
//
// The central contract: a token's RAW value exists only in the response that
// mints it and in the request that presents it. What reaches the database is
// always Hash(raw) — a sha256 digest — so a stolen dump is not a session-hijack
// kit.
//
// sha256 rather than bcrypt is the right call HERE and only here: these tokens
// are 256 bits of crypto/rand output, so there is no low-entropy secret to
// slow an attacker down against, and resolution sits on the hot path of every
// authenticated request. Passwords keep bcrypt (internal/pkg/pwhash) precisely
// because they are low-entropy and human-chosen.
package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math/big"
)

// TokenBytes is the entropy of every opaque token this package mints.
//
// 32 bytes = 256 bits. That is far past the point where brute force matters,
// and it is what lets the storage side use a plain sha256: there is nothing to
// grind. Shrinking this without also revisiting the hash choice would quietly
// turn a fast hash from "correct" into "wrong".
const TokenBytes = 32

// NewToken returns a URL-safe random token and its storage hash.
//
// Callers set the raw value in a cookie or an e-mail link and persist ONLY the
// hash. The two are returned together so no call site can forget to hash, or
// hash a different value than the one it handed out.
func NewToken() (raw string, hash []byte, err error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("secrets: read random: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, Hash(raw), nil
}

// Hash returns the storage digest for a raw token.
//
// It hashes the ENCODED string rather than the underlying bytes so that the
// value hashed on the way out and the value hashed on the way in are literally
// the same string — a lookup can never miss because one side decoded and the
// other did not.
func Hash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// Equal compares two token hashes in constant time.
//
// Token lookups go through a UNIQUE index, so the database comparison is the
// usual path; this exists for the CSRF check, which compares a header against
// the hash already loaded with the session row. That one is a genuine
// secret-vs-secret comparison in application code, and a byte-at-a-time
// compare there is a timing oracle.
func Equal(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// NewNumericCode returns a zero-padded decimal code of the given length,
// drawn uniformly.
//
// The obvious implementation — rand.Read then `% 10` per digit — is biased:
// 256 is not a multiple of 10, so bytes 0..5 map to digits 0..5 one extra time
// each, making low digits ~2.4% likelier. On a 6-digit code that is a small
// but real reduction in the search space, and it is free to avoid.
// crypto/rand.Int does rejection sampling internally.
func NewNumericCode(digits int) (string, error) {
	if digits < 1 || digits > 18 {
		return "", fmt.Errorf("secrets: digits must be between 1 and 18, got %d", digits)
	}
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("secrets: read random: %w", err)
	}
	return fmt.Sprintf("%0*d", digits, n), nil
}

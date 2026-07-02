// Package pwhash is the single bcrypt hash/verify helper shared by every
// password-bearing feature (folder passwords, the master recovery password).
// Keeping it a leaf package avoids one domain importing another just to reuse
// hashing, and guarantees every password in the app uses the same cost and
// comparison. The plaintext is never stored or logged.
package pwhash

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// Hash bcrypt-hashes a plaintext password for storage. Never store the plaintext.
func Hash(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(b), nil
}

// Verify reports whether plain matches the bcrypt hash.
func Verify(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

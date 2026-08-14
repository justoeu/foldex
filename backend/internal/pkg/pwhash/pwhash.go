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

// IsSupported reports whether hash is a structurally valid bcrypt digest at
// the one cost this application generates and accepts.
func IsSupported(hash string) bool {
	if len(hash) != 60 || hash[0] != '$' || hash[1] != '2' || hash[3] != '$' || hash[6] != '$' {
		return false
	}
	switch hash[2] {
	case 'a', 'b', 'y':
	default:
		return false
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil || cost != bcrypt.DefaultCost {
		return false
	}
	for i := 7; i < len(hash); i++ {
		c := hash[i]
		if c != '.' && c != '/' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// Verify reports whether plain matches the bcrypt hash.
func Verify(hash, plain string) bool {
	if !IsSupported(hash) {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

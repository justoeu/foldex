package auth

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// recoveryAlphabet is Crockford base32 minus the characters people transcribe
// wrongly (I, L, O, U). What remains is 32 symbols, which matters twice over:
// it makes each character exactly 5 bits, and — because 32 divides 256 — it
// makes `b % 32` a UNIFORM selection from a random byte.
//
// That is the one place in this codebase where modulo on a random byte is
// correct, and it is worth stating explicitly: secrets.NewNumericCode uses
// rejection sampling precisely because 10 does NOT divide 256 and the modulo
// there would bias digits 0–5.
const recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// recoveryCodeChars is the number of symbols per code: 10 × 5 bits = 50 bits of
// entropy. That is what makes a fast sha256 hash the right choice for storage —
// there is nothing to grind — and it is why the codes are displayed grouped,
// since a human has to copy them onto paper.
const recoveryCodeChars = 10

// newRecoveryCodes returns n freshly generated codes in display form.
//
// The caller hashes them for storage and shows the plaintext exactly once. The
// server cannot reproduce them afterwards, which is the property that makes
// "we cannot e-mail you your recovery codes" an honest statement rather than a
// policy.
func newRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, 0, n)
	for range n {
		buf := make([]byte, recoveryCodeChars)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("generate recovery code: %w", err)
		}
		var sb strings.Builder
		for i, b := range buf {
			// Grouped 5-5 with a hyphen: transcription from paper is the
			// expected path, and unbroken 10-character strings are where people
			// lose their place.
			if i == recoveryCodeChars/2 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryAlphabet[int(b)%len(recoveryAlphabet)])
		}
		codes = append(codes, sb.String())
	}
	return codes, nil
}

// normalizeRecoveryCode makes verification insensitive to how the code was
// typed: case, hyphens and stray whitespace all vanish.
//
// It must be applied identically when hashing for storage and when looking up,
// or every code fails to verify. Both paths call this function for that reason.
func normalizeRecoveryCode(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= 'a' && r <= 'z':
			return r - 32 // to upper
		default:
			return -1
		}
	}, code)
}

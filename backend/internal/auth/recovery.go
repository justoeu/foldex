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

// recoveryCodeChars is the number of symbols per code: 16 x 5 bits = 80 bits of
// entropy. The storage digest is additionally keyed so a database snapshot
// cannot test the finite code space without AUTH_ENCRYPTION_KEY.
const recoveryCodeChars = 16

// newRecoveryCodes returns n freshly generated codes in display form.
//
// The caller hashes them for storage and shows the plaintext exactly once. The
// server cannot reproduce them afterwards, which is the property that makes
// "we cannot e-mail you your recovery codes" an honest statement rather than a
// policy.
func newRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, 0, n)
	for range n {
		// Grouped 4-4-4-4: transcription from paper is the expected path, and
		// unbroken 16-character strings are where people lose their place.
		c, err := randomCode(recoveryCodeChars, 4)
		if err != nil {
			return nil, err
		}
		codes = append(codes, c)
	}
	return codes, nil
}

// randomCode returns `symbols` characters from recoveryAlphabet, hyphenated
// every `group` characters (group <= 0 disables grouping).
func randomCode(symbols, group int) (string, error) {
	buf := make([]byte, symbols)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	var sb strings.Builder
	for i, b := range buf {
		if group > 0 && i > 0 && i%group == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(recoveryAlphabet[int(b)%len(recoveryAlphabet)])
	}
	return sb.String(), nil
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

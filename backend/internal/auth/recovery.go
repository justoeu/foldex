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
		// Grouped 5-5: transcription from paper is the expected path, and
		// unbroken 10-character strings are where people lose their place.
		c, err := randomCode(recoveryCodeChars, recoveryCodeChars/2)
		if err != nil {
			return nil, err
		}
		codes = append(codes, c)
	}
	return codes, nil
}

// temporaryPasswordChars is 15 symbols × 5 bits = 75 bits, comfortably more
// than the 50 a recovery code carries. The extra margin is because this value
// is a PASSWORD: it is typed into a login form that an attacker can reach,
// where a recovery code is only usable once a first factor already passed.
const temporaryPasswordChars = 15

// newTemporaryPassword mints the credential an administrator hands to a user
// who can no longer sign in.
//
// The same alphabet as recovery codes, for the same reason: it will be read
// aloud or copied by hand, and I/L/O/U are the characters that get transcribed
// wrongly. It is shown to the admin exactly once — nothing stores the
// plaintext.
func newTemporaryPassword() (string, error) {
	return randomCode(temporaryPasswordChars, 5)
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

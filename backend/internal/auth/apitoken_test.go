package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseAPIToken is the front door for a credential an attacker supplies
// verbatim, so every shape it can be handed has to end somewhere definite.
// Anything it accepts becomes a primary-key lookup; anything it rejects is a
// 401 indistinguishable from every other 401.
func TestParseAPIToken(t *testing.T) {
	t.Parallel()

	id, secret, ok := parseAPIToken("fx_42_thesecret")
	require.True(t, ok)
	assert.Equal(t, int64(42), id)
	assert.Equal(t, "thesecret", secret)

	// The secret half is base64url and may itself contain underscores. Splitting
	// greedily would truncate it and turn a valid token into a 401 that looks
	// like a wrong secret.
	id, secret, ok = parseAPIToken("fx_7_abc_def_ghi")
	require.True(t, ok)
	assert.Equal(t, int64(7), id)
	assert.Equal(t, "abc_def_ghi", secret)

	// Surrounding whitespace is what a header value picks up from a config file
	// or a copy-paste.
	id, _, ok = parseAPIToken("  fx_9_secret\n")
	require.True(t, ok)
	assert.Equal(t, int64(9), id)
}

func TestParseAPIToken_RejectsEveryMalformedShape(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		"",
		"garbage",
		"fx_",
		"fx_secret",                      // no id separator
		"fx__secret",                     // empty id
		"fx_12_",                         // empty secret
		"fx_abc_secret",                  // non-numeric id
		"fx_0_secret",                    // ids are BIGSERIAL, which starts at 1
		"fx_-3_secret",                   // a negative id would be a nonsense lookup
		"FX_1_secret",                    // the prefix is case-SENSITIVE: a scanner greps the literal
		"Bearer fx_1_secret",             // the scheme is stripped before we get here
		"fx_99999999999999999999_secret", // overflows int64
	} {
		_, _, ok := parseAPIToken(bad)
		assert.False(t, ok, "accepted %q", bad)
	}
}

// The prefix is not decoration: secret scanners, CI log filters and grep all
// key on a literal, and an opaque base64 blob is indistinguishable from every
// other base64 blob in a log file.
func TestAPITokenPrefixIsStableAndDistinctive(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "fx_", APITokenPrefix)
}

// ─────────────────────────────────────────────────────────────────────
// Temporary passwords
// ─────────────────────────────────────────────────────────────────────

// The temporary password is read aloud or copied by hand from an administrator
// to a locked-out user, so it uses the recovery-code alphabet — Crockford
// base32 minus I, L, O and U, the four characters people transcribe wrongly.
func TestNewTemporaryPassword(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 50 {
		pw, err := newTemporaryPassword()
		require.NoError(t, err)
		require.False(t, seen[pw], "temporary password repeated: %q", pw)
		seen[pw] = true

		assert.Equal(t, "XXXXX-XXXXX-XXXXX", shape(pw), "unexpected shape: %q", pw)
		for _, r := range strings.ReplaceAll(pw, "-", "") {
			assert.Contains(t, recoveryAlphabet, string(r),
				"character %q is outside the transcription-safe alphabet", r)
		}
		// It has to survive the same policy a user-chosen password does,
		// otherwise the administrator hands over something the login refuses.
		assert.NoError(t, validatePassword(pw))
	}
}

// 15 symbols × 5 bits = 75 bits, comfortably more than a recovery code's 50.
// The margin is because this value is typed into a login form an attacker can
// reach, where a recovery code is only usable once a first factor already
// passed.
func TestTemporaryPasswordCarriesMoreEntropyThanARecoveryCode(t *testing.T) {
	t.Parallel()
	assert.Greater(t, temporaryPasswordChars, recoveryCodeChars)
}

func TestRandomCodeWithoutGrouping(t *testing.T) {
	t.Parallel()
	c, err := randomCode(8, 0)
	require.NoError(t, err)
	assert.Len(t, c, 8)
	assert.NotContains(t, c, "-")
}

// shape renders a string with every alphabet character replaced by X, so a
// test can assert the grouping without pinning the random content.
func shape(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' {
			return r
		}
		return 'X'
	}, s)
}

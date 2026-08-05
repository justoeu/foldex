package secrets_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/secrets"
)

func TestNewTokenReturnsMatchingHash(t *testing.T) {
	t.Parallel()
	raw, hash, err := secrets.NewToken()
	require.NoError(t, err)

	assert.Equal(t, secrets.Hash(raw), hash,
		"the returned hash must be the hash OF the returned token — a mismatch means every lookup misses")
}

// The token must decode to exactly TokenBytes of entropy. A shorter token would
// still "work" — every test would pass — while quietly making the fast sha256
// storage hash the wrong choice.
func TestNewTokenCarriesFullEntropy(t *testing.T) {
	t.Parallel()
	raw, _, err := secrets.NewToken()
	require.NoError(t, err)

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	require.NoError(t, err, "token must be valid base64url")
	assert.Len(t, decoded, secrets.TokenBytes)
}

func TestNewTokenIsURLSafe(t *testing.T) {
	t.Parallel()
	// 200 draws is enough to hit every base64 alphabet position with
	// overwhelming probability; a token carrying '+' or '/' would break the
	// invite link it is pasted into.
	for range 200 {
		raw, _, err := secrets.NewToken()
		require.NoError(t, err)
		assert.False(t, strings.ContainsAny(raw, "+/="),
			"token %q must be URL-safe: it is embedded in e-mail links", raw)
	}
}

func TestNewTokenIsUnique(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 500)
	for range 500 {
		raw, _, err := secrets.NewToken()
		require.NoError(t, err)
		_, dup := seen[raw]
		require.False(t, dup, "crypto/rand returned a duplicate token — entropy source is broken")
		seen[raw] = struct{}{}
	}
}

func TestHashIsDeterministicAndDistinct(t *testing.T) {
	t.Parallel()
	assert.Equal(t, secrets.Hash("abc"), secrets.Hash("abc"))
	assert.NotEqual(t, secrets.Hash("abc"), secrets.Hash("abd"))
	assert.Len(t, secrets.Hash("abc"), 32, "sha256 digest is 32 bytes")
}

func TestEqual(t *testing.T) {
	t.Parallel()
	a := secrets.Hash("token")
	assert.True(t, secrets.Equal(a, secrets.Hash("token")))
	assert.False(t, secrets.Equal(a, secrets.Hash("other")))
	assert.False(t, secrets.Equal(a, nil), "a nil hash must never compare equal")
	assert.False(t, secrets.Equal(a, a[:16]), "a prefix must not compare equal")
}

func TestNewNumericCodeShape(t *testing.T) {
	t.Parallel()
	for _, digits := range []int{4, 6, 8} {
		code, err := secrets.NewNumericCode(digits)
		require.NoError(t, err)
		assert.Len(t, code, digits, "short draws must stay zero-padded, not shrink")
		for _, r := range code {
			assert.True(t, r >= '0' && r <= '9', "code %q must be all digits", code)
		}
	}
}

func TestNewNumericCodeRejectsSillyLengths(t *testing.T) {
	t.Parallel()
	_, err := secrets.NewNumericCode(0)
	assert.Error(t, err)
	_, err = secrets.NewNumericCode(19)
	assert.Error(t, err, "19 digits overflows the int64 the exponent is built from")
}

// The distribution guard. A `rand.Read() % 10` implementation biases digits
// 0–5 upward by ~2.4% each, because 256 is not a multiple of 10. Over 20k
// single-digit draws that bias is far outside sampling noise, so this test
// fails loudly if someone "simplifies" the rejection sampling away.
func TestNewNumericCodeIsNotModuloBiased(t *testing.T) {
	t.Parallel()
	const draws = 20000
	counts := make(map[rune]int, 10)
	for range draws {
		code, err := secrets.NewNumericCode(1)
		require.NoError(t, err)
		counts[rune(code[0])]++
	}
	expected := draws / 10
	// ±15% tolerance: comfortably wider than the ~1.7% standard deviation of a
	// fair draw at this sample size, and comfortably narrower than the ~28%
	// gap a modulo-biased generator produces between its most and least likely
	// digits.
	for d := '0'; d <= '9'; d++ {
		assert.InDelta(t, expected, counts[d], float64(expected)*0.15,
			"digit %c appeared %d times in %d draws — distribution looks biased", d, counts[d], draws)
	}
}

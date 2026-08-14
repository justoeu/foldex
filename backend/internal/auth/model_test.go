package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/mailer"
	"foldex/internal/pkg/pwhash"
)

// NormalizeEmail is duplicated as a CHECK constraint on app_user
// (email_normalized = lower(btrim(email))). A drift between the Go function and
// the SQL is not a search bug — it is two accounts that should have been one,
// or a login that resolves the wrong row.
func TestNormalizeEmail(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"User@Example.COM":   "user@example.com",
		"  spaced@x.com  ":   "spaced@x.com",
		"\tTabbed@X.com\n":   "tabbed@x.com",
		"already@lower.com":  "already@lower.com",
		"MiXeD@CaSe.Example": "mixed@case.example",
		"":                   "",
	}
	for in, want := range cases {
		assert.Equal(t, want, NormalizeEmail(in), "input %q", in)
	}
}

// btrim in SQL strips ASCII spaces and tabs/newlines the same way
// strings.TrimSpace does for these inputs; anything more exotic would need the
// CHECK to be revisited, so pin the agreement explicitly.
func TestNormalizeEmailIsIdempotent(t *testing.T) {
	t.Parallel()
	for _, in := range []string{" A@B.com ", "a@b.com", "\tX@Y.Z\n"} {
		once := NormalizeEmail(in)
		assert.Equal(t, once, NormalizeEmail(once), "normalizing twice must not change the value")
	}
}

func TestMaskEmail(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"alice@example.com":              "al•••@example.com",
		"ab@example.com":                 "a•@example.com",
		"a@example.com":                  "•@example.com",
		"averylonglocalpart@example.com": "av••••••@example.com",
	}
	for in, want := range cases {
		assert.Equal(t, want, MaskEmail(in), "input %q", in)
	}
}

// A value with no @ must not leak itself. The masked form is shown to whoever
// triggered a 2FA challenge, who has not yet proven they own the account.
func TestMaskEmailOnMalformedInput(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "no-at-sign", "@leading"} {
		got := MaskEmail(in)
		assert.Equal(t, "•••", got, "input %q must not be echoed back", in)
	}
}

// The masked form must never reveal the full local part, at any length.
func TestMaskEmailNeverExposesTheWholeLocalPart(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"a@x.com", "ab@x.com", "abc@x.com", "abcdefghijkl@x.com"} {
		assert.NotContains(t, MaskEmail(in), in[:len(in)-len("@x.com")]+"@",
			"the full local part of %q leaked", in)
	}
}

// The dummy-hash burn, measured with the duration floor out of the way.
//
// The floor is what actually erases the timing signal in production, but it
// also HIDES whether the burn is still there: with it applied, deleting
// `pwhash.Verify(dummyHash, …)` leaves both branches at 250 ms and every
// floor-based assertion keeps passing while the ~80 ms enumeration oracle
// quietly returns. Dropping the floor to zero makes the burn the only thing
// left to measure.
func TestLoginBurnsBcryptEvenForAnUnknownEmail(t *testing.T) {
	// Measured on burnDummyHash directly. That the LOGIN HANDLER calls it on the
	// miss path is covered by the integration suite; what needs proving here is
	// that the call costs real work, because that is the half the floor hides.
	// bcrypt at DefaultCost is tens of milliseconds; a skipped verify returns in
	// microseconds, so the gap is unmistakable.
	start := time.Now()
	burnDummyHash("whatever the attacker typed")
	elapsed := time.Since(start)

	assert.Greater(t, elapsed, 10*time.Millisecond,
		"comparing against the dummy hash must cost real bcrypt work (took %v) — "+
			"skipping it for an unknown address is the classic ~80ms enumeration oracle", elapsed)
}

func TestDummyHashIsARealBcryptHashThatMatchesNothingUseful(t *testing.T) {
	assert.True(t, strings.HasPrefix(dummyHash, "$2a$"), "must be a real bcrypt hash, not a placeholder")
	assert.False(t, pwhash.Verify(dummyHash, ""), "the dummy must not accept an empty password")
	assert.False(t, pwhash.Verify(dummyHash, "password"), "the dummy must not accept a common password")
}

// newLimiterOnlyHandler builds a Handler for tests that only touch its
// in-memory rate-limit buckets. The repository is nil on purpose: nothing on
// this path reaches the database, and wiring one would need a container for a
// test about maps.
func newLimiterOnlyHandler(t *testing.T) *Handler {
	t.Helper()
	m, err := mailer.New(mailer.Config{Driver: "log"}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	d := mailer.NewDispatcher(context.Background(), m, mailer.DispatcherOptions{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	t.Cleanup(d.Stop)
	return NewHandler(HandlerConfig{
		Mailer:         m,
		MailDispatcher: d,
		TTL:            DefaultTTL(),
		Logger:         slog.New(slog.NewJSONHandler(io.Discard, nil)),
		BaseURL:        "https://foldex.test",
	})
}

func TestWriteSessionInvalidClearsCookies(t *testing.T) {
	h := newLimiterOnlyHandler(t)
	rec := httptest.NewRecorder()
	h.writeSessionInvalid(rec)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"session_expired"`)
	assert.Len(t, rec.Result().Cookies(), 4)
}

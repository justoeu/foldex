package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFactorTxnError is the ONE mapping every factor endpoint routes through;
// this table is the parity guarantee the four email_factor endpoints (and any
// future one) inherit instead of re-implementing.
func TestWriteFactorTxnErrorParity(t *testing.T) {
	h := &Handler{cookies: CookieOptions{}, logger: slog.New(slog.DiscardHandler)}

	for name, tc := range map[string]struct {
		err      error
		handled  bool
		status   int
		contains string
	}{
		"nil":               {nil, false, 0, ""},
		"challenge":         {ErrChallengeInvalid, true, http.StatusUnauthorized, `"code":"challenge_invalid"`},
		"session":           {ErrSessionInvalid, true, http.StatusUnauthorized, `"code":"session_expired"`},
		"no enrollment":     {ErrNoPendingFactor, true, http.StatusConflict, `"code":"email_factor_not_enabled"`},
		"unknown":           {errors.New("boom"), true, http.StatusInternalServerError, `"code":"internal"`},
		"wrapped challenge": {fmtErrWrap{"w: %w", ErrChallengeInvalid}, true, http.StatusUnauthorized, `"code":"challenge_invalid"`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handled := h.writeFactorTxnError(rec, tc.err, "test ctx")
			assert.Equal(t, tc.handled, handled)
			if tc.err == nil {
				return
			}
			require.Equal(t, tc.status, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.contains)
		})
	}
}

type fmtErrWrap struct {
	format string
	cause  error
}

func (e fmtErrWrap) Error() string { return "wrapped" }

func (e fmtErrWrap) Unwrap() error { return e.cause }

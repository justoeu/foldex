package auth

import (
	"errors"
	"time"

	"foldex/internal/auth/twofa"
)

// verifyTOTP translates the twofa package's mismatch into the auth
// package's credential error so handlers see one vocabulary.
func verifyTOTP(secretB32, code string, params twofa.Params, now time.Time) (int64, error) {
	n, err := twofa.Verify(secretB32, code, params, now)
	if errors.Is(err, twofa.ErrMismatch) {
		return 0, ErrBadCredentials
	}
	return n, err
}

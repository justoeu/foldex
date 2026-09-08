package auth

import (
	"errors"
	"time"

	"github.com/pquerna/otp"

	"foldex/internal/auth/twofa"
)

const (
	totpAlgorithm     = twofa.Algorithm
	totpDigits        = twofa.Digits
	totpPeriodSeconds = twofa.PeriodSeconds
	totpSkew          = twofa.Skew
	recoveryCodeCount = twofa.RecoveryCount
	recoveryCodeChars = twofa.CodeChars
	recoveryAlphabet  = twofa.Alphabet
)

var (
	ErrTOTPParams = twofa.ErrParams
	ErrTOTPReplay = twofa.ErrReplay
)

type totpParams = twofa.Params

func newTOTPSecret(issuer, account string) (*otp.Key, error) {
	return twofa.NewSecret(issuer, account)
}

func totpQRPNG(key *otp.Key, size int) ([]byte, error) {
	return twofa.QRPNG(key, size)
}

func normalizeOTPCode(code string) string { return twofa.NormalizeOTP(code) }

func verifyTOTP(secretB32, code string, params totpParams, now time.Time) (int64, error) {
	n, err := twofa.Verify(secretB32, code, params, now)
	if errors.Is(err, twofa.ErrMismatch) {
		return 0, ErrBadCredentials
	}
	return n, err
}

func otpauthURL(issuer, account, secretB32 string) string {
	return twofa.URL(issuer, account, secretB32)
}

func numericOTP(code string) (string, bool) { return twofa.NumericOTP(code) }

func newRecoveryCodes(n int) ([]string, error) { return twofa.NewRecoveryCodes(n) }

func randomCode(symbols, group int) (string, error) { return twofa.RandomCode(symbols, group) }

func normalizeRecoveryCode(code string) string { return twofa.NormalizeRecovery(code) }

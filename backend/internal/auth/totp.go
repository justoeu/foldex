package auth

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"image/png"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

// TOTP parameters are PINNED, not configurable.
//
// Google Authenticator, Authy, 1Password and Bitwarden all silently ignore
// non-default `algorithm` and `digits` values in an otpauth:// URI: the app
// enrols happily and then produces codes that never validate, with no error
// anywhere to explain it. The schema admits other values for a future in which
// that changes; the code refuses them today so the failure is a boot-time
// mismatch rather than a user who cannot sign in.
const (
	totpAlgorithm     = "SHA1"
	totpDigits        = 6
	totpPeriodSeconds = 30

	// totpSkew accepts one step either side of now — ±30 s of clock drift
	// between the server and the user's phone. Wider windows multiply the
	// number of codes valid at any instant, which is exactly what the attempt
	// cap exists to constrain.
	totpSkew = 1
)

// recoveryCodeCount is how many single-use codes are minted per enrollment.
const recoveryCodeCount = 10

var (
	// ErrTOTPParams marks a stored secret whose parameters are not the pinned
	// ones. Verification refuses rather than guessing, because guessing wrong
	// rejects every correct code the user types.
	ErrTOTPParams = errors.New("auth: unsupported TOTP parameters")
	// ErrTOTPReplay marks a code that is arithmetically valid but belongs to a
	// time step already consumed.
	ErrTOTPReplay = errors.New("auth: TOTP code already used")
)

// newTOTPSecret mints an enrollment secret and its otpauth:// key.
//
// account is the user's e-mail: it is what the authenticator app displays, and
// with several foldex instances (or several accounts) a user needs to tell the
// entries apart.
func newTOTPSecret(issuer, account string) (*otp.Key, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		Period:      totpPeriodSeconds,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, fmt.Errorf("generate totp secret: %w", err)
	}
	return key, nil
}

// totpQRPNG renders the otpauth:// URI as a PNG.
//
// Server-side on purpose: it keeps the base32 seed off the wire in any form a
// JavaScript QR library would need, and it adds no frontend dependency. The
// cost is one indirect Go dependency (boombuler/barcode).
func totpQRPNG(key *otp.Key, size int) ([]byte, error) {
	img, err := key.Image(size, size)
	if err != nil {
		return nil, fmt.Errorf("totp qr image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("totp qr encode: %w", err)
	}
	return buf.Bytes(), nil
}

// normalizeOTPCode strips the formatting humans and password managers add.
//
// Authenticator apps display "123 456", 1Password copies "123456", and a user
// retyping from a recovery sheet may add hyphens. Rejecting those is a support
// burden with no security value — the code's entropy is unchanged.
func normalizeOTPCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, code)
}

// totpParams is the stored shape of one enrollment.
type totpParams struct {
	Algorithm string
	Digits    int
	Period    int
}

func (p totpParams) pinned() bool {
	return p.Algorithm == totpAlgorithm && p.Digits == totpDigits && p.Period == totpPeriodSeconds
}

// verifyTOTP checks code against secret and returns the time-step counter it
// matched.
//
// Returning the counter is what makes the replay guard possible. A bare
// "valid / not valid" answer leaves a code reusable for the remainder of its
// own 30-second window, so anyone who reads it over the user's shoulder — or
// off a phishing page a second earlier — can still spend it.
func verifyTOTP(secretB32, code string, params totpParams, now time.Time) (int64, error) {
	if !params.pinned() {
		return 0, ErrTOTPParams
	}
	code = normalizeOTPCode(code)
	if len(code) != totpDigits {
		return 0, ErrBadCredentials
	}

	base := now.Unix() / totpPeriodSeconds
	for delta := int64(-totpSkew); delta <= totpSkew; delta++ {
		counter := base + delta
		if counter < 0 {
			continue
		}
		want, err := hotp.GenerateCodeCustom(secretB32, uint64(counter), hotp.ValidateOpts{
			Digits:    otp.DigitsSix,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			// A malformed stored secret. Treated as a credential failure so the
			// endpoint's response shape does not change, but it is a server
			// fault and the caller logs it.
			return 0, fmt.Errorf("totp generate: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return counter, nil
		}
	}
	return 0, ErrBadCredentials
}

// otpauthURL rebuilds the enrollment URI from a stored seed.
//
// The QR endpoint needs it because only the base32 secret is persisted, not the
// URI. Every parameter is written explicitly rather than relying on library
// defaults, so a future change to those defaults cannot silently produce a URI
// that disagrees with what verifyTOTP enforces.
func otpauthURL(issuer, account, secretB32 string) string {
	v := url.Values{}
	v.Set("secret", secretB32)
	v.Set("issuer", issuer)
	v.Set("algorithm", totpAlgorithm)
	v.Set("digits", strconv.Itoa(totpDigits))
	v.Set("period", strconv.Itoa(totpPeriodSeconds))
	// The label is "issuer:account" and the colon is the separator, so both
	// halves are escaped as one path segment.
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

// numericOTP reports whether the input is a six-digit code, and returns it
// stripped of the separators people and apps put in.
//
// The test is on the length AFTER removing only whitespace and hyphens — never
// after removing every non-digit. The difference decides how a recovery code is
// routed: "3F7K-9MQX-Z2AB-CDEF" compacts to sixteen symbols and is not a numeric
// code, whereas filtering it down to its digits could leave exactly six and
// send it to the TOTP path, where it can never match.
func numericOTP(code string) (string, bool) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		// Non-breaking space included: it is what a copy from a rendered HTML
		// mail often carries, and it is invisible to the user reporting that
		// "the code just does not work".
		case ' ', '\t', '\n', '\r', '-', '.', '\u00a0':
			return -1
		}
		return r
	}, code)
	if len(compact) != totpDigits {
		return "", false
	}
	for _, r := range compact {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return compact, true
}

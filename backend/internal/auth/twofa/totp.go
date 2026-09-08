package twofa

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

const (
	Algorithm     = "SHA1"
	Digits        = 6
	PeriodSeconds = 30
	Skew          = 1
	RecoveryCount = 10
)

var (
	ErrParams   = errors.New("auth: unsupported TOTP parameters")
	ErrReplay   = errors.New("auth: TOTP code already used")
	ErrMismatch = errors.New("auth: invalid credentials")
)

func NewSecret(issuer, account string) (*otp.Key, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		Period:      PeriodSeconds,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, fmt.Errorf("generate totp secret: %w", err)
	}
	return key, nil
}

func QRPNG(key *otp.Key, size int) ([]byte, error) {
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

func NormalizeOTP(code string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, code)
}

type Params struct {
	Algorithm string
	Digits    int
	Period    int
}

func (p Params) Pinned() bool {
	return p.Algorithm == Algorithm && p.Digits == Digits && p.Period == PeriodSeconds
}

func Verify(secretB32, code string, params Params, now time.Time) (int64, error) {
	if !params.Pinned() {
		return 0, ErrParams
	}
	code = NormalizeOTP(code)
	if len(code) != Digits {
		return 0, ErrMismatch
	}

	base := now.Unix() / PeriodSeconds
	for delta := int64(-Skew); delta <= Skew; delta++ {
		counter := base + delta
		if counter < 0 {
			continue
		}
		want, err := hotp.GenerateCodeCustom(secretB32, uint64(counter), hotp.ValidateOpts{
			Digits:    otp.DigitsSix,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			return 0, fmt.Errorf("totp generate: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return counter, nil
		}
	}
	return 0, ErrMismatch
}

func URL(issuer, account, secretB32 string) string {
	v := url.Values{}
	v.Set("secret", secretB32)
	v.Set("issuer", issuer)
	v.Set("algorithm", Algorithm)
	v.Set("digits", strconv.Itoa(Digits))
	v.Set("period", strconv.Itoa(PeriodSeconds))
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

func NumericOTP(code string) (string, bool) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '-', '.', '\u00a0':
			return -1
		}
		return r
	}, code)
	if len(compact) != Digits {
		return "", false
	}
	for _, r := range compact {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return compact, true
}

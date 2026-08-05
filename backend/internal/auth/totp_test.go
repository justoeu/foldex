package auth

import (
	"bytes"
	"errors"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
)

func pinnedParams() totpParams {
	return totpParams{Algorithm: totpAlgorithm, Digits: totpDigits, Period: totpPeriodSeconds}
}

// codeFor produces the code for an exact counter, so the tests can address a
// specific time step instead of hoping wall-clock lands where they need it.
func codeFor(t *testing.T, secret string, counter int64) string {
	t.Helper()
	code, err := hotp.GenerateCodeCustom(secret, uint64(counter), hotp.ValidateOpts{
		Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	return code
}

const testSeed = "JBSWY3DPEHPK3PXP"

func TestVerifyTOTPAcceptsTheCurrentStepAndReturnsItsCounter(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	want := now.Unix() / totpPeriodSeconds

	got, err := verifyTOTP(testSeed, codeFor(t, testSeed, want), pinnedParams(), now)
	if err != nil {
		t.Fatalf("verifyTOTP: %v", err)
	}
	// The counter is the whole point of the return value: it is what the replay
	// guard stores, and a wrong one either rejects the next code or lets this
	// one be spent twice.
	if got != want {
		t.Fatalf("counter = %d, want %d", got, want)
	}
}

// ±1 step of drift is accepted; ±2 is not. A wider window multiplies the number
// of codes valid at any instant, which is exactly what the attempt cap exists
// to constrain.
func TestVerifyTOTPSkewWindow(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	base := now.Unix() / totpPeriodSeconds

	for _, delta := range []int64{-1, 0, 1} {
		if _, err := verifyTOTP(testSeed, codeFor(t, testSeed, base+delta), pinnedParams(), now); err != nil {
			t.Fatalf("delta %+d should be inside the skew window: %v", delta, err)
		}
	}
	for _, delta := range []int64{-2, 2, 10} {
		if _, err := verifyTOTP(testSeed, codeFor(t, testSeed, base+delta), pinnedParams(), now); err == nil {
			t.Fatalf("delta %+d should be outside the skew window", delta)
		}
	}
}

func TestVerifyTOTPRejectsWrongShapes(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	for _, code := range []string{"", "12345", "1234567", "abcdef", "     "} {
		if _, err := verifyTOTP(testSeed, code, pinnedParams(), now); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("code %q: want ErrBadCredentials, got %v", code, err)
		}
	}
}

// Stored parameters that are not the pinned set are REFUSED rather than
// guessed at. Guessing wrong rejects every correct code the user types, with
// nothing to explain it.
func TestVerifyTOTPRefusesUnsupportedParameters(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	valid := codeFor(t, testSeed, now.Unix()/totpPeriodSeconds)

	for name, p := range map[string]totpParams{
		"sha256":    {Algorithm: "SHA256", Digits: 6, Period: 30},
		"8 digits":  {Algorithm: "SHA1", Digits: 8, Period: 30},
		"60s step":  {Algorithm: "SHA1", Digits: 6, Period: 60},
		"all empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifyTOTP(testSeed, valid, p, now); !errors.Is(err, ErrTOTPParams) {
				t.Fatalf("want ErrTOTPParams, got %v", err)
			}
		})
	}
}

// The code is displayed as "123 456", copied as "123456" and sometimes typed
// with a hyphen. All three are the same code.
func TestVerifyTOTPAcceptsFormattedInput(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	raw := codeFor(t, testSeed, now.Unix()/totpPeriodSeconds)

	for _, formatted := range []string{
		raw,
		raw[:3] + " " + raw[3:],
		raw[:3] + "-" + raw[3:],
		" " + raw + "\n",
	} {
		if _, err := verifyTOTP(testSeed, formatted, pinnedParams(), now); err != nil {
			t.Fatalf("%q should verify: %v", formatted, err)
		}
	}
}

func TestVerifyTOTPRejectsAMalformedStoredSecret(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	// Not valid base32. The error is wrapped rather than ErrBadCredentials so
	// the caller can log it as the server fault it is.
	_, err := verifyTOTP("!!!not base32!!!", "123456", pinnedParams(), now)
	if err == nil {
		t.Fatal("a malformed seed must not silently verify")
	}
	if errors.Is(err, ErrBadCredentials) {
		t.Fatal("a malformed stored seed is a server fault, not a wrong code")
	}
}

// The routing rule that decides which credential the user typed. Getting this
// wrong made ~1 in 23 recovery codes unusable; see the integration test that
// constructs that case.
func TestNumericOTP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"123456", "123456", true},
		{"123 456", "123456", true},
		{"123-456", "123456", true},
		{"123.456", "123456", true},
		{" 123456 ", "123456", true}, // non-breaking space from an HTML mail
		{" 123456 ", "123456", true},
		{"12345", "", false},
		{"1234567", "", false},
		{"", "", false},
		{"12345a", "", false},
		// The shapes that matter: a recovery code, with and without exactly six
		// digits among its ten symbols. NEITHER may be treated as numeric.
		{"1A2B3-4C5D6", "", false},
		{"ABCDE-FGHJK", "", false},
		{"12345-67890", "", false},
	}
	for _, tc := range cases {
		got, ok := numericOTP(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("numericOTP(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	t.Parallel()
	// Case, hyphens and whitespace all vanish, because storage and lookup both
	// go through this function — a mismatch between them means every code fails.
	for _, in := range []string{"1A2B3-4C5D6", "1a2b3-4c5d6", " 1a2b3 4c5d6 ", "1A2B34C5D6"} {
		if got := normalizeRecoveryCode(in); got != "1A2B34C5D6" {
			t.Fatalf("normalizeRecoveryCode(%q) = %q", in, got)
		}
	}
	if got := normalizeRecoveryCode("---"); got != "" {
		t.Fatalf("a separator-only input should normalize to empty, got %q", got)
	}
}

func TestNewRecoveryCodesShape(t *testing.T) {
	t.Parallel()
	codes, err := newRecoveryCodes(recoveryCodeCount)
	if err != nil {
		t.Fatalf("newRecoveryCodes: %v", err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(codes), recoveryCodeCount)
	}

	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate code %q in one sheet", c)
		}
		seen[c] = true

		if len(c) != recoveryCodeChars+1 || c[recoveryCodeChars/2] != '-' {
			t.Fatalf("code %q is not grouped 5-5 with a hyphen", c)
		}
		// The alphabet excludes I, L, O and U precisely because they are the
		// characters people mistranscribe from paper.
		for _, r := range strings.ReplaceAll(c, "-", "") {
			if !strings.ContainsRune(recoveryAlphabet, r) {
				t.Fatalf("code %q contains %q, outside the alphabet", c, r)
			}
		}
		// And a code must never be mistaken for a numeric one.
		if _, ok := numericOTP(c); ok {
			t.Fatalf("recovery code %q was classified as a six-digit code", c)
		}
	}
}

// Two sheets must not overlap: the codes are the fallback credential, so a
// generator that repeats itself would hand one user another's way in.
func TestNewRecoveryCodesAreUnpredictable(t *testing.T) {
	t.Parallel()
	a, err := newRecoveryCodes(recoveryCodeCount)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newRecoveryCodes(recoveryCodeCount)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, c := range a {
		set[c] = true
	}
	for _, c := range b {
		if set[c] {
			t.Fatalf("code %q appeared in two independent sheets", c)
		}
	}
}

func TestNewTOTPSecretAndQR(t *testing.T) {
	t.Parallel()
	key, err := newTOTPSecret("Foldex (test)", "user@example.com")
	if err != nil {
		t.Fatalf("newTOTPSecret: %v", err)
	}
	if key.Secret() == "" {
		t.Fatal("empty seed")
	}
	// The account name is what an authenticator app shows; without it a user
	// with two entries cannot tell which code belongs to which.
	if !strings.Contains(key.URL(), "user%40example.com") && !strings.Contains(key.URL(), "user@example.com") {
		t.Fatalf("otpauth URL does not carry the account: %s", key.URL())
	}

	img, err := totpQRPNG(key, 200)
	if err != nil {
		t.Fatalf("totpQRPNG: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(img)); err != nil {
		t.Fatalf("QR is not a decodable PNG: %v", err)
	}
}

// The QR endpoint rebuilds the URI from the stored seed, so the rebuilt one has
// to agree with what the enrollment handed out — otherwise a user who scans the
// QR enrols a different secret than the one the server will verify against.
func TestOTPAuthURLRoundTripsThroughTheKeyParser(t *testing.T) {
	t.Parallel()
	const issuer = "Foldex (test)"
	const account = "user@example.com"

	key, err := otp.NewKeyFromURL(otpauthURL(issuer, account, testSeed))
	if err != nil {
		t.Fatalf("rebuilt URL does not parse: %v", err)
	}
	if key.Secret() != testSeed {
		t.Fatalf("seed = %q, want %q", key.Secret(), testSeed)
	}
	if key.Issuer() != issuer {
		t.Fatalf("issuer = %q, want %q", key.Issuer(), issuer)
	}
}

func TestNormalizeOTPCodeKeepsOnlyDigits(t *testing.T) {
	t.Parallel()
	if got := normalizeOTPCode(" 1a2-3 4b5.6 "); got != "123456" {
		t.Fatalf("normalizeOTPCode = %q", got)
	}
	if got := normalizeOTPCode("abc"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

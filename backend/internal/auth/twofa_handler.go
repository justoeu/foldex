package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/pquerna/otp"

	"foldex/internal/mailer"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/secrets"
)

// challengeTTL bounds the pre-auth window. Long enough to fetch a phone,
// short enough that an abandoned half-login is not a standing credential.
const challengeTTL = 10 * time.Minute

// emailOTPTTL is how long a mailed code stays usable.
const emailOTPTTL = 5 * time.Minute

// twoFactorMethod names, echoed to the SPA so the code screen can offer the
// right alternatives.
const (
	methodTOTP     = "totp"
	methodRecovery = "recovery_code"
	methodEmailOTP = "email_otp"
)

// ─────────────────────────────────────────────────────────────────────
// The login divert
// ─────────────────────────────────────────────────────────────────────

// secondFactorPurpose decides what, if anything, stands between a correct
// password and a session.
//
// Returns "" when the password IS the whole login. Otherwise it returns the
// challenge purpose: 'totp' for an account with a confirmed authenticator, or
// 'enroll_2fa' for an administrator who has not set one up while the policy
// requires it.
//
// The enroll case is what makes AUTH_REQUIRE_2FA_FOR_ADMINS meaningful rather
// than advisory. Refusing the login outright would lock every existing admin
// out the moment the flag flips; letting them in "just this once" is a rule
// that never applies. Diverting into a mandatory enrollment is the only option
// that both admits them and enforces the policy.
func (h *Handler) secondFactorPurpose(u User) ChallengePurpose {
	// A nil cipher means the 2FA stack is not wired at all; there is nothing to
	// verify a code against, so a challenge here would be a dead end. The guard
	// lives INSIDE this function rather than beside each call so a future third
	// caller cannot copy the check and drop half of it.
	if h.cipher == nil || h.codeMAC == nil {
		return ""
	}
	if u.TOTPEnabled {
		return PurposeTOTP
	}
	if h.require2FAForAdmins && u.Role.IsAdmin() {
		return PurposeEnroll2FA
	}
	return ""
}

// emailFactorAvailable reports whether a mailed code may serve as the second
// factor for this challenge.
//
// Two things disqualify it, and both are the same mistake in different clothes
// — letting one channel satisfy both steps:
//
//  1. The FIRST factor was a password-reset link, so the attacker who read that
//     link would read the code too.
//  2. The `log` mail driver, which prints the message body to stdout. That is a
//     deliberate, documented trade for INVITE links (the log is the mailbox on
//     an instance with no SMTP), but a second factor written to the container
//     log is readable by anyone with the docker group or a log shipper — the
//     factor stops being a factor.
func (h *Handler) emailFactorAvailable(purpose ChallengePurpose, mailboxAlreadyProven bool) bool {
	return purpose == PurposeTOTP && !mailboxAlreadyProven && h.mailer.Driver() == "smtp"
}

// completeLogin issues a session, or diverts into the second factor.
//
// Every credential path that ends in "the first factor is proven" funnels
// through here — login, password reset and the OAuth callback — so
// there is exactly one place that decides whether a proven password IS a login.
func (h *Handler) completeLogin(w http.ResponseWriter, r *http.Request, user User, mailboxAlreadyProven bool) {
	if purpose := h.secondFactorPurpose(user); purpose != "" {
		h.startChallenge(w, r, user, purpose, mailboxAlreadyProven)
		return
	}
	h.issueAndRespond(w, r, user)
}

// beginChallenge mints the pre-auth cookie, returning false once it has already
// written an error response.
//
// Separate from startChallenge because the OAuth callback needs the cookie
// without a JSON body: it answers a top-level browser navigation and must end
// in a redirect, not in a payload the browser would render as text.
func (h *Handler) beginChallenge(w http.ResponseWriter, r *http.Request, u User, purpose ChallengePurpose, mailboxAlreadyProven bool) bool {
	return h.beginChallengeFor(w, r, NewChallenge{
		UserID:               u.ID,
		Purpose:              purpose,
		TokenVersion:         u.TokenVersion,
		TTL:                  challengeTTL,
		IP:                   clientIP(r),
		UserAgent:            r.UserAgent(),
		MailboxAlreadyProven: mailboxAlreadyProven,
	})
}

// beginChallengeFor is the full-control form, used by the OAuth callback to
// pin the Google subject onto the row it creates.
func (h *Handler) beginChallengeFor(w http.ResponseWriter, r *http.Request, in NewChallenge) bool {
	raw, _, err := h.repo.CreateChallenge(r.Context(), in)
	if err != nil {
		h.logger.Error("create challenge", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return false
	}
	h.cookies.SetPreAuth(w, raw, challengeTTL)
	return true
}

var errInvalidChallengePurpose = errors.New("auth: invalid challenge purpose")

// pendingPayload describes a half-finished login.
//
// Built in one place because two endpoints emit it: the credential path that
// creates the challenge, and /me — which reports it on a cold boot so a reload
// during the code step, or the fresh page load an OAuth redirect produces,
// lands back on the code screen instead of on the login form.
func (h *Handler) pendingPayload(u User, purpose ChallengePurpose, mailboxAlreadyProven bool) (authWireResponse, error) {
	// The address is masked because a successful credential-stuffing attempt
	// must not receive the confirmed full address. The raw pre-auth token stays
	// exclusively in its httpOnly cookie.
	switch purpose {
	case PurposeTOTP:
		methods := []string{methodTOTP, methodRecovery}
		if h.emailFactorAvailable(purpose, mailboxAlreadyProven) {
			methods = append(methods, methodEmailOTP)
		}
		return twoFactorAuthResponse{
			Status: statusTwoFactorRequired, Purpose: purpose, Email: MaskEmail(u.Email),
			Methods: methods, ExpiresIn: int(challengeTTL.Seconds()),
			MaxAttempts: maxChallengeAttempts, Features: h.features,
		}, nil
	case PurposeEnroll2FA:
		return enrollmentAuthResponse{
			Status: statusTwoFactorRequired, Purpose: purpose, Email: MaskEmail(u.Email),
			Methods: []string{}, ExpiresIn: int(challengeTTL.Seconds()),
			MaxAttempts: maxChallengeAttempts, Features: h.features,
			Reason: "admin_enrollment_required",
		}, nil
	case PurposeConvertGoogle:
		// Not a second factor at all: the account owes its CURRENT PASSWORD
		// before the Google identity is attached. Reusing the two_factor status
		// would put the SPA on the six-digit code screen.
		return conversionAuthResponse{
			Status: statusConvertPasswordAccount, Purpose: purpose, Email: MaskEmail(u.Email),
			Methods: []string{}, ExpiresIn: int(challengeTTL.Seconds()),
			MaxAttempts: maxChallengeAttempts, Features: h.features,
		}, nil
	default:
		return nil, errInvalidChallengePurpose
	}
}

// startChallenge mints the pre-auth cookie and writes the pending payload.
func (h *Handler) startChallenge(w http.ResponseWriter, r *http.Request, u User, purpose ChallengePurpose, mailboxAlreadyProven bool) {
	payload, err := h.pendingPayload(u, purpose, mailboxAlreadyProven)
	if err != nil {
		h.logger.Error("build challenge response", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if !h.beginChallenge(w, r, u, purpose, mailboxAlreadyProven) {
		return
	}
	httperr.JSON(w, http.StatusOK, payload)
}

// ─────────────────────────────────────────────────────────────────────
// Verifying a second factor
// ─────────────────────────────────────────────────────────────────────

type verifyInput struct {
	Code string `json:"code"`
}

// Verify2FA accepts a TOTP code, a recovery code or a mailed OTP — one endpoint
// for all three, because the user is looking at one six-character field and
// does not think of them as different systems.
//
// Every failure produces the same 401. Telling the caller "that is not a valid
// TOTP code but it might be a recovery code" narrows their search for free.
func (h *Handler) Verify2FA(w http.ResponseWriter, r *http.Request) {
	// The floor applies here for the same reason it applies to login: the three
	// verification paths cost wildly different amounts of work (a hash compare,
	// an indexed lookup, a second indexed lookup), and the difference would say
	// which kind of code was recognised.
	defer floorDuration(time.Now(), loginFloor)

	ch, ok := h.requireChallenge(w, r, PurposeTOTP)
	if !ok {
		return
	}
	in, err := httperr.DecodeJSON[verifyInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	// Charge the attempt BEFORE checking the code. Charging afterwards means a
	// request cancelled mid-flight — or a handler that panics — costs the
	// attacker nothing, and 5 attempts becomes unbounded.
	attempts, err := h.repo.BumpChallengeAttempt(r.Context(), ch.ID)
	if err != nil {
		h.writeChallengeError(w, err)
		return
	}

	user, err := h.repo.GetUser(r.Context(), ch.UserID)
	if err != nil {
		h.logger.Error("2fa load user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// Re-check the account status. Login checked it, but that was up to ten
	// minutes ago: an administrator who disables an account in the meantime
	// expects the half-finished login to die with it, not to complete.
	if user.Status != StatusActive {
		_ = h.repo.ConsumeChallenge(r.Context(), ch.ID)
		h.cookies.ClearPreAuth(w)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "challenge_invalid",
			"this sign-in attempt expired; start again"))
		return
	}

	proof := h.secondFactorProof(r.Context(), user, in.Code, &ch.ID)
	tok, method, err := h.repo.Complete2FA(r.Context(), ch, proof, h.ttl, clientIP(r), r.UserAgent())
	if errors.Is(err, ErrBadCredentials) {
		remaining := maxChallengeAttempts - attempts
		if remaining < 0 {
			remaining = 0
		}
		if remaining == 0 {
			// Keep the exhausted row live until its window ends so another
			// correct-password login cannot mint a fresh set of guesses.
			h.cookies.ClearPreAuth(w)
			httperr.Write(w, httperr.New(http.StatusTooManyRequests, "too_many_attempts",
				"too many incorrect codes; try again later"))
			return
		}
		httperr.JSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]string{
				"code":    "invalid_code",
				"message": "that code is not valid",
			},
			"attempts_remaining": remaining,
		})
		return
	}
	if err != nil {
		h.writeChallengeError(w, err)
		return
	}
	if method == methodRecovery {
		// Spending a recovery code is either a user with a new phone or an
		// attacker holding the printed sheet. The owner is the only one who can
		// tell, and only if told.
		h.notifyRecoveryCodeUsed(r.Context(), user)
	}
	h.cookies.ClearPreAuth(w)
	h.cookies.SetSession(w, tok)
	httperr.JSON(w, http.StatusOK, h.authenticatedPayload(user, tok.CSRF))
}

// secondFactorProof prepares every proof the submitted shape can represent.
// The repository decides which one is live while consuming it with the
// challenge and session write in one transaction.
func (h *Handler) secondFactorProof(ctx context.Context, u User, code string,
	challengeID *int64) secondFactorProof {

	// The two kinds are told apart by the SEPARATOR-STRIPPED length, not by how
	// many digits survive a digit-only filter.
	//
	// Filtering to digits and asking "is it six long?" misroutes every recovery
	// code that happens to contain exactly six digits. With sixteen symbols from
	// this alphabet that is roughly 18% of codes, and none could be redeemed at
	// all. A recovery code is sixteen symbols once its hyphens are removed, so
	// length separates them cleanly while still accepting "123 456" and
	// "123-456" as a numeric code.
	if digits, ok := numericOTP(code); ok {
		return secondFactorProof{
			totp:        h.verifyTOTPProof(ctx, u.ID, digits),
			emailDigest: h.codeMAC.EmailOTPDigest(u.ID, OTPPurposeLogin2FA, challengeID, digits),
		}
	}
	normalized := normalizeRecoveryCode(code)
	if normalized == "" {
		return secondFactorProof{}
	}
	return secondFactorProof{
		recoveryDigest: h.codeMAC.RecoveryCodeDigest(u.ID, normalized),
	}
}

// verifyTOTPProof performs only the cryptographic check. The repository burns
// the returned counter together with the protected mutation.
func (h *Handler) verifyTOTPProof(ctx context.Context, uid authctx.UserID, code string) *TOTPProof {
	row, err := h.repo.LoadTOTPSecret(ctx, uid)
	if err != nil || !row.Confirmed {
		return nil
	}
	secret, err := h.cipher.Decrypt(row.Ciphertext, row.Nonce)
	if err != nil {
		// An undecryptable seed means the encryption key changed. That is an
		// operator emergency, not a wrong code, and it is logged as such — but
		// the response stays identical, because the caller is unauthenticated.
		h.logger.Error("totp secret cannot be decrypted — AUTH_ENCRYPTION_KEY may have changed",
			"user_id", int64(uid))
		return nil
	}
	counter, err := verifyTOTP(string(secret), code, row.Params, time.Now())
	if err != nil {
		if errors.Is(err, ErrTOTPParams) {
			h.logger.Error("stored TOTP parameters are not the supported set", "user_id", int64(uid))
		}
		return nil
	}
	proof := TOTPProof{Counter: counter, Ciphertext: row.Ciphertext, Nonce: row.Nonce}
	if h.afterTOTPVerification != nil {
		h.afterTOTPVerification(ctx, uid, proof)
	}
	return &proof
}

// SendEmailOTP mails a one-time code for a pending challenge.
//
// Answers 202 for a refusal by rate limit as well as for a real send, so the
// endpoint cannot be used to probe how many codes have already gone out.
func (h *Handler) SendEmailOTP(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.requireChallenge(w, r, PurposeTOTP)
	if !ok {
		return
	}
	// Refuse rather than silently 202: this is the door the reset flow closes,
	// and answering "accepted" while sending nothing would leave the user
	// waiting for a code that is never coming.
	if !h.emailFactorAvailable(ch.Purpose, ch.MailboxAlreadyProven) {
		httperr.Write(w, httperr.New(http.StatusForbidden, "email_factor_unavailable",
			"a mailed code cannot be used for this sign-in"))
		return
	}
	admission, err := h.dispatcher.Reserve()
	if err != nil {
		writeMailQueueUnavailable(w)
		return
	}
	code, err := secrets.NewNumericCode(totpDigits)
	if err != nil {
		admission.Release()
		h.logger.Error("otp generate", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	email, err := h.repo.EmailForUser(r.Context(), ch.UserID)
	if err != nil {
		admission.Release()
		h.logger.Error("otp recipient", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	id := ch.ID
	ttl := h.otpTTL(r.Context())
	if _, err := h.repo.CreateChallengeEmailOTP(r.Context(), ch.ID,
		h.codeMAC.EmailOTPDigest(ch.UserID, OTPPurposeLogin2FA, &id, code),
		ttl, h.otpCooldown(r.Context())); err != nil {
		admission.Release()
		switch {
		case errors.Is(err, ErrTooSoon), errors.Is(err, ErrSendsExhausted):
			w.WriteHeader(http.StatusAccepted)
		default:
			h.writeChallengeError(w, err)
		}
		return
	}
	if err := admission.Publish(
		// The MAILED lifetime is the one just persisted, not the constant: a
		// message promising five minutes for a code that expires in two is a
		// support ticket wearing a feature.
		mailer.LoginCodeMessage(email, code, int(ttl.Minutes())), "login otp"); err != nil {
		h.logger.Error("publish login otp mail", "err", err)
	}
	w.WriteHeader(http.StatusAccepted)
}

// ─────────────────────────────────────────────────────────────────────
// Enrollment
// ─────────────────────────────────────────────────────────────────────

type totpStartInput struct {
	// Password is required when enrolling from a live session. It is NOT
	// required in the enroll_2fa pre-auth flow, where the password was proven
	// moments ago to obtain the challenge.
	Password string `json:"password"`
}

// StartTOTP mints an enrollment secret and returns the base32 seed plus the
// otpauth:// URI.
//
// Requires proof of the password even for an authenticated caller: adding a
// second factor with nothing but a hijacked cookie would let an attacker lock
// the real owner out of their own account.
func (h *Handler) StartTOTP(w http.ResponseWriter, r *http.Request) {
	uid, tokenVersion, sessionID, ok := h.enrollmentPrincipal(w, r)
	if !ok {
		return
	}
	email, err := h.repo.EmailForUser(r.Context(), uid)
	if err != nil {
		h.logger.Error("totp start recipient", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	key, err := newTOTPSecret(h.totpIssuer, email)
	if err != nil {
		h.logger.Error("totp generate", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	ciphertext, nonce, err := h.cipher.Encrypt([]byte(key.Secret()))
	if err != nil {
		h.logger.Error("totp encrypt", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if err := h.repo.StartTOTPEnrollment(r.Context(), uid, tokenVersion, sessionID, ciphertext, nonce); err != nil {
		if errors.Is(err, ErrTOTPAlreadyConfirmed) {
			httperr.Write(w, httperr.New(http.StatusConflict, "totp_already_enabled",
				"two-factor authentication is already enabled; disable it first"))
			return
		}
		if errors.Is(err, ErrChallengeInvalid) {
			h.writeChallengeError(w, err)
			return
		}
		h.logger.Error("totp store", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{
		"secret":  key.Secret(),
		"otpauth": key.URL(),
		"issuer":  h.totpIssuer,
		"account": email,
		"digits":  totpDigits,
		"period":  totpPeriodSeconds,
		"qr_url":  "/api/auth/2fa/totp/qr.png",
	})
}

// TOTPQR renders the pending enrollment's otpauth:// URI as a PNG.
//
// Rendering server-side keeps the seed out of any JavaScript QR library and
// adds no frontend dependency. no-store because the image IS the secret in
// visual form — a cached copy in a shared browser profile is a copy of the
// second factor.
func (h *Handler) TOTPQR(w http.ResponseWriter, r *http.Request) {
	uid, tokenVersion, sessionID, ok := h.enrollmentPrincipalNoPassword(w, r)
	if !ok {
		return
	}
	row, err := h.repo.LoadTOTPSecret(r.Context(), uid)
	if err != nil || row.Confirmed {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	if row.EnrollmentTokenVersion == nil || *row.EnrollmentTokenVersion != tokenVersion {
		h.writeChallengeError(w, ErrChallengeInvalid)
		return
	}
	if !enrollmentSessionMatches(row.EnrollmentSessionID, sessionID) {
		h.writeChallengeError(w, ErrChallengeInvalid)
		return
	}
	secret, err := h.cipher.Decrypt(row.Ciphertext, row.Nonce)
	if err != nil {
		h.logger.Error("totp qr decrypt", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	email, err := h.repo.EmailForUser(r.Context(), uid)
	if err != nil {
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	key, err := otp.NewKeyFromURL(otpauthURL(h.totpIssuer, email, string(secret)))
	if err != nil {
		h.logger.Error("totp qr key", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	png, err := totpQRPNG(key, 240)
	if err != nil {
		h.logger.Error("totp qr render", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

type totpConfirmInput struct {
	Code string `json:"code"`
}

// ConfirmTOTP activates a pending enrollment and returns the recovery codes.
//
// The codes are shown EXACTLY once. The server stores only their keyed MAC, so it
// genuinely cannot show them again — which is the property that makes "write
// these down now" an honest instruction rather than a scare.
func (h *Handler) ConfirmTOTP(w http.ResponseWriter, r *http.Request) {
	uid, tokenVersion, sessionID, ok := h.enrollmentPrincipalNoPassword(w, r)
	if !ok {
		return
	}
	in, err := httperr.DecodeJSON[totpConfirmInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	row, err := h.repo.LoadTOTPSecret(r.Context(), uid)
	if err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "no_enrollment",
			"start an enrollment first"))
		return
	}
	if row.Confirmed {
		httperr.Write(w, httperr.New(http.StatusConflict, "totp_already_enabled",
			"two-factor authentication is already enabled"))
		return
	}
	if row.EnrollmentTokenVersion == nil || *row.EnrollmentTokenVersion != tokenVersion {
		h.writeChallengeError(w, ErrChallengeInvalid)
		return
	}
	if !enrollmentSessionMatches(row.EnrollmentSessionID, sessionID) {
		h.writeChallengeError(w, ErrChallengeInvalid)
		return
	}
	secret, err := h.cipher.Decrypt(row.Ciphertext, row.Nonce)
	if err != nil {
		h.logger.Error("totp confirm decrypt", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	counter, err := verifyTOTP(string(secret), in.Code, row.Params, time.Now())
	if err != nil {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_code",
			"that code is not valid"))
		return
	}
	codes, hashes, err := h.newRecoveryCodeSet(uid)
	if err != nil {
		h.logger.Error("recovery codes", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	var challenge *Challenge
	if _, authenticated := authctx.FromContext(r.Context()); !authenticated {
		ch, err := h.repo.ResolveChallenge(r.Context(), cookieValue(r, CookiePreAuth), PurposeEnroll2FA)
		if err != nil {
			h.writeChallengeError(w, err)
			return
		}
		challenge = &ch
	}
	user, tok, err := h.repo.CompleteTOTPEnrollment(r.Context(), uid, tokenVersion,
		TOTPProof{Counter: counter, Ciphertext: row.Ciphertext, Nonce: row.Nonce}, hashes,
		sessionID, challenge, h.ttl, clientIP(r), r.UserAgent())
	if err != nil {
		if errors.Is(err, ErrTOTPEnrollmentChanged) {
			httperr.Write(w, httperr.New(http.StatusConflict, "enrollment_changed",
				"the enrollment changed; verify the current authenticator secret"))
			return
		}
		if errors.Is(err, ErrChallengeInvalid) {
			h.writeChallengeError(w, err)
			return
		}
		if errors.Is(err, ErrSessionInvalid) {
			h.writeSessionInvalid(w)
			return
		}
		h.logger.Error("totp confirm", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	// Enrolling from the pre-auth (admin enrollment) flow completes the login:
	// the password was proven to get the challenge and the authenticator has
	// just been proven too, so there is nothing left to ask for.
	if challenge != nil {
		h.cookies.ClearPreAuth(w)
		h.cookies.SetSession(w, tok)
		payload := h.authenticatedPayload(user, tok.CSRF)
		payload.RecoveryCodes = codes
		httperr.JSON(w, http.StatusOK, payload)
		return
	}

	httperr.JSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

type totpDisableInput struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// DisableTOTP removes the second factor. Requires BOTH the password and a
// current code — the same two proofs enrolling it demanded, so a stolen session
// alone cannot strip the protection it cannot satisfy.
func (h *Handler) DisableTOTP(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	in, err := httperr.DecodeJSON[totpDisableInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	user, err := h.repo.GetUser(r.Context(), p.UserID)
	if err != nil {
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// Policy check first: an admin who could disable their own second factor
	// would make AUTH_REQUIRE_2FA_FOR_ADMINS a suggestion. The next login would
	// divert them straight back into mandatory enrollment anyway.
	if h.require2FAForAdmins && user.Role.IsAdmin() {
		httperr.Write(w, httperr.New(http.StatusForbidden, "totp_required_for_admins",
			"administrators must keep two-factor authentication enabled"))
		return
	}
	proof, stepUpKey, ok := h.stepUpTOTPProof(w, r, p.UserID, in.Code)
	if !ok {
		return
	}
	err = h.repo.DisableTOTP(r.Context(), p.UserID, p.SessionID, user.TokenVersion, in.Password, *proof)
	h.settleStepUp(stepUpKey, err)
	if errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrPasswordMissing) {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
			"password is incorrect"))
		return
	}
	if errors.Is(err, ErrTOTPReplay) {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_code", "that code is not valid"))
		return
	}
	if errors.Is(err, ErrSessionInvalid) {
		h.writeSessionInvalid(w)
		return
	}
	if err != nil {
		h.logger.Error("disable totp", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type regenerateInput struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// RegenerateRecoveryCodes replaces the whole set, invalidating the old sheet.
func (h *Handler) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	in, err := httperr.DecodeJSON[regenerateInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	user, err := h.repo.GetUser(r.Context(), p.UserID)
	if err != nil {
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	proof, stepUpKey, ok := h.stepUpTOTPProof(w, r, p.UserID, in.Code)
	if !ok {
		return
	}
	codes, hashes, err := h.newRecoveryCodeSet(p.UserID)
	if err != nil {
		h.stepUpUser.Release(stepUpKey)
		h.logger.Error("regenerate recovery codes", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	err = h.repo.RegenerateRecoveryCodes(r.Context(), p.UserID, p.SessionID, user.TokenVersion,
		in.Password, *proof, hashes)
	h.settleStepUp(stepUpKey, err)
	if errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrPasswordMissing) {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
			"password is incorrect"))
		return
	}
	if errors.Is(err, ErrTOTPReplay) {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_code", "that code is not valid"))
		return
	}
	if errors.Is(err, ErrSessionInvalid) {
		h.writeSessionInvalid(w)
		return
	}
	if err != nil {
		h.logger.Error("regenerate recovery codes", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// TwoFactorStatus reports the caller's own 2FA state for the settings screen.
func (h *Handler) TwoFactorStatus(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	user, err := h.repo.GetUser(r.Context(), p.UserID)
	if err != nil {
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	remaining, err := h.repo.CountRecoveryCodes(r.Context(), p.UserID)
	if err != nil {
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{
		"enabled":                  user.TOTPEnabled,
		"recovery_codes_remaining": remaining,
		"required":                 h.require2FAForAdmins && user.Role.IsAdmin(),
	})
}

// ─────────────────────────────────────────────────────────────────────
// Shared helpers
// ─────────────────────────────────────────────────────────────────────

// stepUpTOTPProof verifies a current TOTP code under an attempt budget and
// returns the exact row/counter proof for transactional consumption.
//
// The budget is what separates these endpoints from Verify2FA, which is capped
// by auth_challenge.attempts. There is no challenge on a session-authenticated
// step-up, so without a limiter an attacker holding a hijacked session could
// grind the ~3 codes valid at any instant out of a space of a million.
func (h *Handler) stepUpTOTPProof(w http.ResponseWriter, r *http.Request,
	uid authctx.UserID, code string) (*TOTPProof, string, bool) {

	key := "stepup:" + strconv.FormatInt(int64(uid), 10)
	until, ok := h.stepUpUser.Begin(key)
	if !ok {
		writeRateLimited(w, until)
		return nil, "", false
	}
	proof := h.verifyTOTPProof(r.Context(), uid, normalizeOTPCode(code))
	if proof == nil {
		h.stepUpUser.CommitFail(key)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_code", "that code is not valid"))
		return nil, "", false
	}
	return proof, key, true
}

func (h *Handler) settleStepUp(key string, err error) {
	switch {
	case err == nil:
		h.stepUpUser.CommitSuccess(key)
	case errors.Is(err, ErrTOTPReplay), errors.Is(err, ErrBadCredentials),
		errors.Is(err, ErrPasswordMissing), errors.Is(err, ErrSessionInvalid):
		h.stepUpUser.CommitFail(key)
	default:
		h.stepUpUser.Release(key)
	}
}

// requireChallenge resolves the pre-auth cookie or writes the refusal.
func (h *Handler) requireChallenge(w http.ResponseWriter, r *http.Request, purposes ...ChallengePurpose) (Challenge, bool) {
	ch, err := h.repo.ResolveChallenge(r.Context(), cookieValue(r, CookiePreAuth), purposes...)
	if err != nil {
		h.writeChallengeError(w, err)
		return Challenge{}, false
	}
	return ch, true
}

func (h *Handler) writeChallengeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrChallengeWrongPurpose):
		// The challenge is alive, just not for this endpoint — an enroll_2fa
		// user who lands on /2fa/verify, say. Clearing the cookie here would
		// destroy a usable credential and force them to start over.
		httperr.Write(w, httperr.New(http.StatusConflict, "wrong_challenge",
			"this sign-in attempt is at a different step"))
	case errors.Is(err, ErrChallengeExhausted):
		h.cookies.ClearPreAuth(w)
		httperr.Write(w, httperr.New(http.StatusTooManyRequests, "too_many_attempts",
			"too many incorrect codes; try again later"))
	case errors.Is(err, ErrChallengeInvalid):
		h.cookies.ClearPreAuth(w)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "challenge_invalid",
			"this sign-in attempt expired; start again"))
	default:
		h.logger.Error("challenge", "err", err)
		httperr.Write(w, httperr.ErrInternal)
	}
}

func (h *Handler) writeSessionInvalid(w http.ResponseWriter) {
	h.cookies.ClearSession(w)
	httperr.Write(w, httperr.New(http.StatusUnauthorized, "session_expired", "session expired"))
}

// enrollmentPrincipal resolves who is enrolling, demanding a password from a
// session-authenticated caller.
//
// Two callers reach the enrollment endpoints: a signed-in user adding a factor
// from settings, and an admin diverted into mandatory enrollment mid-login. The
// second has no session yet and has already proven the password; the first has
// a session and must prove it again.
func (h *Handler) enrollmentPrincipal(w http.ResponseWriter, r *http.Request) (authctx.UserID, int, int64, bool) {
	if p, ok := authctx.FromContext(r.Context()); ok {
		in, err := httperr.DecodeJSON[totpStartInput](w, r)
		if err != nil {
			httperr.Write(w, err)
			return 0, 0, 0, false
		}
		tokenVersion, err := h.repo.VerifyUserPasswordEpoch(r.Context(), p.UserID, in.Password)
		if err != nil {
			if errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrPasswordMissing) {
				httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
					"password is incorrect"))
			} else {
				h.logger.Error("verify enrollment password", "err", err)
				httperr.Write(w, httperr.ErrInternal)
			}
			return 0, 0, 0, false
		}
		return p.UserID, tokenVersion, p.SessionID, true
	}
	ch, ok := h.requireChallenge(w, r, PurposeEnroll2FA)
	if !ok {
		return 0, 0, 0, false
	}
	return ch.UserID, ch.TokenVersion, 0, true
}

// enrollmentPrincipalNoPassword is the same resolution for the endpoints that
// act on an ALREADY-CREATED pending enrollment (the QR image and the confirm
// step), where the password was proven by the call that created it.
func (h *Handler) enrollmentPrincipalNoPassword(w http.ResponseWriter, r *http.Request) (authctx.UserID, int, int64, bool) {
	if p, ok := authctx.FromContext(r.Context()); ok {
		user, err := h.repo.GetUser(r.Context(), p.UserID)
		if err != nil {
			httperr.Write(w, httperr.ErrInternal)
			return 0, 0, 0, false
		}
		return p.UserID, user.TokenVersion, p.SessionID, true
	}
	ch, ok := h.requireChallenge(w, r, PurposeEnroll2FA)
	if !ok {
		return 0, 0, 0, false
	}
	return ch.UserID, ch.TokenVersion, 0, true
}

func enrollmentSessionMatches(stored *int64, current int64) bool {
	if current == 0 {
		return stored == nil
	}
	return stored != nil && *stored == current
}

// newRecoveryCodeSet generates a fresh display set and its stored digests.
func (h *Handler) newRecoveryCodeSet(uid authctx.UserID) ([]string, [][]byte, error) {
	codes, err := newRecoveryCodes(recoveryCodeCount)
	if err != nil {
		return nil, nil, err
	}
	hashes := make([][]byte, 0, len(codes))
	for _, c := range codes {
		// MAC the NORMALIZED form, because verification normalizes too. Storing
		// the display form with its hyphen would make every typed code fail.
		hashes = append(hashes, h.codeMAC.RecoveryCodeDigest(uid, normalizeRecoveryCode(c)))
	}
	return codes, hashes, nil
}

func (h *Handler) notifyRecoveryCodeUsed(ctx context.Context, u User) {
	remaining, err := h.repo.CountRecoveryCodes(ctx, u.ID)
	if err != nil {
		return
	}
	h.enqueueMail(mailer.RecoveryCodeUsedMessage(u.Email, remaining), "recovery code notification")
}

func (h *Handler) enqueueMail(msg mailer.Message, what string) {
	if err := h.dispatcher.Enqueue(msg, what); err != nil {
		h.logger.Warn("mail not queued", "what", what, "err", err)
	}
}

func writeMailQueueUnavailable(w http.ResponseWriter) {
	httperr.Write(w, httperr.New(http.StatusServiceUnavailable, "mail_queue_full",
		"mail delivery is busy; try again shortly"))
}

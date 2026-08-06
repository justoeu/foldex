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
func (h *Handler) secondFactorPurpose(u User) string {
	// A nil cipher means the 2FA stack is not wired at all; there is nothing to
	// verify a code against, so a challenge here would be a dead end. The guard
	// lives INSIDE this function rather than beside each call so a future third
	// caller cannot copy the check and drop half of it.
	if h.cipher == nil {
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
func (h *Handler) emailFactorAvailable(purpose string, mailboxAlreadyProven bool) bool {
	return purpose == PurposeTOTP && !mailboxAlreadyProven && h.mailer.Driver() == "smtp"
}

// completeLogin issues a session, or diverts into the second factor.
//
// Every credential path that ends in "the first factor is proven" funnels
// through here — login, password reset, and (from PR4) the OAuth callback — so
// there is exactly one place that decides whether a proven password IS a login.
func (h *Handler) completeLogin(w http.ResponseWriter, r *http.Request, user User, mailboxAlreadyProven bool) {
	if purpose := h.secondFactorPurpose(user); purpose != "" {
		h.startChallenge(w, r, user, purpose, mailboxAlreadyProven)
		return
	}
	h.issueAndRespond(w, r, user)
}

// startChallenge mints the pre-auth cookie and writes the pending payload.
func (h *Handler) startChallenge(w http.ResponseWriter, r *http.Request, u User, purpose string, mailboxAlreadyProven bool) {
	raw, _, err := h.repo.CreateChallenge(r.Context(), u.ID, purpose, challengeTTL,
		clientIP(r), r.UserAgent(), mailboxAlreadyProven)
	if err != nil {
		h.logger.Error("create challenge", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.cookies.SetPreAuth(w, raw, challengeTTL)

	methods := []string{methodTOTP, methodRecovery}
	if h.emailFactorAvailable(purpose, mailboxAlreadyProven) {
		methods = append(methods, methodEmailOTP)
	}
	body := map[string]any{
		"status":  "two_factor_required",
		"purpose": purpose,
		// The address is MASKED. The caller has proven the password, but this
		// response is also what an attacker sees after a successful credential
		// stuffing hit — echoing the full address hands them the confirmed
		// pairing for free.
		"email":   MaskEmail(u.Email),
		"methods": methods,
		// The raw token is NOT echoed here. It travels only in the httpOnly,
		// Strict, /api/auth-scoped cookie SetPreAuth just wrote — putting it in
		// the body would hand it to any script on the page and to whatever
		// client-side error reporter happens to capture a response.
		"expires_in":   int(challengeTTL.Seconds()),
		"max_attempts": maxChallengeAttempts,
	}
	if purpose == PurposeEnroll2FA {
		body["methods"] = []string{}
		body["reason"] = "admin_enrollment_required"
	}
	httperr.JSON(w, http.StatusOK, body)
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

	switch method := h.checkSecondFactor(r.Context(), user, in.Code, &ch.ID); method {
	case "":
		remaining := maxChallengeAttempts - attempts
		if remaining < 0 {
			remaining = 0
		}
		if remaining == 0 {
			// The budget is gone: kill the challenge so the pre-auth cookie is
			// inert, and make the user re-enter their password. That re-runs
			// the login rate limiters, which are the durable cap.
			_ = h.repo.ConsumeChallenge(r.Context(), ch.ID)
			h.cookies.ClearPreAuth(w)
			httperr.Write(w, httperr.New(http.StatusTooManyRequests, "too_many_attempts",
				"too many incorrect codes; sign in again"))
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

	case methodRecovery:
		// Spending a recovery code is either a user with a new phone or an
		// attacker holding the printed sheet. The owner is the only one who can
		// tell, and only if told.
		h.notifyRecoveryCodeUsed(user)
	}

	if err := h.repo.ConsumeChallenge(r.Context(), ch.ID); err != nil {
		h.logger.Error("consume challenge", "err", err)
	}
	h.cookies.ClearPreAuth(w)
	h.issueAndRespond(w, r, user)
}

// checkSecondFactor tries every accepted credential and reports which one
// matched, or "" for none.
//
// Order is cheapest-and-most-common first. Each branch consumes its credential
// on success, so a code cannot be spent twice by racing two requests — the
// atomicity lives in the repository's conditional UPDATEs.
func (h *Handler) checkSecondFactor(ctx context.Context, u User, code string, challengeID *int64) string {
	// The two kinds are told apart by the SEPARATOR-STRIPPED length, not by how
	// many digits survive a digit-only filter.
	//
	// Filtering to digits and asking "is it six long?" misroutes every recovery
	// code that happens to contain exactly six digits — with a 10-character code
	// drawn from a 32-symbol alphabet, that is roughly one in twenty-three of
	// them, and such a code could never be redeemed at all. A recovery code is
	// ten symbols once its hyphen is removed, so the length alone separates them
	// cleanly while still accepting "123 456" and "123-456" as a numeric code.
	if digits, ok := numericOTP(code); ok {
		if h.checkTOTP(ctx, u.ID, digits) {
			return methodTOTP
		}
		// A six-digit code that is not a valid TOTP may still be a mailed OTP.
		if err := h.repo.ConsumeEmailOTP(ctx, u.ID, OTPPurposeLogin2FA, secrets.Hash(digits), challengeID); err == nil {
			return methodEmailOTP
		}
		return ""
	}
	normalized := normalizeRecoveryCode(code)
	if normalized == "" {
		return ""
	}
	if err := h.repo.ConsumeRecoveryCode(ctx, u.ID, secrets.Hash(normalized)); err == nil {
		return methodRecovery
	}
	return ""
}

// checkTOTP verifies a code and burns its time step.
func (h *Handler) checkTOTP(ctx context.Context, uid authctx.UserID, code string) bool {
	row, err := h.repo.LoadTOTPSecret(ctx, uid)
	if err != nil || !row.Confirmed {
		return false
	}
	secret, err := h.cipher.Decrypt(row.Ciphertext, row.Nonce)
	if err != nil {
		// An undecryptable seed means the encryption key changed. That is an
		// operator emergency, not a wrong code, and it is logged as such — but
		// the response stays identical, because the caller is unauthenticated.
		h.logger.Error("totp secret cannot be decrypted — AUTH_ENCRYPTION_KEY may have changed",
			"user_id", int64(uid))
		return false
	}
	counter, err := verifyTOTP(string(secret), code, row.Params, time.Now())
	if err != nil {
		if errors.Is(err, ErrTOTPParams) {
			h.logger.Error("stored TOTP parameters are not the supported set", "user_id", int64(uid))
		}
		return false
	}
	// The replay guard is the DATABASE's conditional update, not this check.
	if err := h.repo.ConsumeTOTPCounter(ctx, uid, counter); err != nil {
		return false
	}
	return true
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
	if _, err := h.repo.ReserveChallengeSend(r.Context(), ch.ID); err != nil {
		switch {
		case errors.Is(err, ErrTooSoon), errors.Is(err, ErrSendsExhausted):
			w.WriteHeader(http.StatusAccepted)
		default:
			h.writeChallengeError(w, err)
		}
		return
	}

	code, err := secrets.NewNumericCode(totpDigits)
	if err != nil {
		h.logger.Error("otp generate", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	id := ch.ID
	if err := h.repo.CreateEmailOTP(r.Context(), ch.UserID, &id, OTPPurposeLogin2FA,
		secrets.Hash(code), emailOTPTTL); err != nil {
		h.logger.Error("otp store", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	email, err := h.repo.EmailForUser(r.Context(), ch.UserID)
	if err != nil {
		h.logger.Error("otp recipient", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.sendAsync(mailer.LoginCodeMessage(email, code, int(emailOTPTTL.Minutes())), "login otp")
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
	uid, ok := h.enrollmentPrincipal(w, r)
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
	if err := h.repo.StartTOTPEnrollment(r.Context(), uid, ciphertext, nonce); err != nil {
		if errors.Is(err, ErrTOTPAlreadyConfirmed) {
			httperr.Write(w, httperr.New(http.StatusConflict, "totp_already_enabled",
				"two-factor authentication is already enabled; disable it first"))
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
	uid, ok := h.enrollmentPrincipalNoPassword(w, r)
	if !ok {
		return
	}
	row, err := h.repo.LoadTOTPSecret(r.Context(), uid)
	if err != nil || row.Confirmed {
		httperr.Write(w, httperr.ErrNotFound)
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
// The codes are shown EXACTLY once. The server stores only their sha256, so it
// genuinely cannot show them again — which is the property that makes "write
// these down now" an honest instruction rather than a scare.
func (h *Handler) ConfirmTOTP(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.enrollmentPrincipalNoPassword(w, r)
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
	if err := h.repo.ConfirmTOTP(r.Context(), uid, counter); err != nil {
		h.logger.Error("totp confirm", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	codes, err := h.mintRecoveryCodes(r.Context(), uid)
	if err != nil {
		h.logger.Error("recovery codes", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	// Enrolling from the pre-auth (admin enrollment) flow completes the login:
	// the password was proven to get the challenge and the authenticator has
	// just been proven too, so there is nothing left to ask for.
	if ch, err := h.repo.ResolveChallenge(r.Context(), cookieValue(r, CookiePreAuth), PurposeEnroll2FA); err == nil {
		_ = h.repo.ConsumeChallenge(r.Context(), ch.ID)
		h.cookies.ClearPreAuth(w)
		user, uerr := h.repo.GetUser(r.Context(), uid)
		if uerr != nil {
			httperr.Write(w, httperr.ErrInternal)
			return
		}
		tok, _, ierr := h.repo.IssueSession(r.Context(), uid, h.ttl, clientIP(r), r.UserAgent())
		if ierr != nil {
			h.logger.Error("issue session after enrollment", "err", ierr)
			httperr.Write(w, httperr.ErrInternal)
			return
		}
		h.cookies.SetSession(w, tok)
		payload := h.authenticatedPayload(user, tok.CSRF)
		payload["recovery_codes"] = codes
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
	if !h.verifyPasswordOr401(w, r, p.UserID, in.Password) {
		return
	}
	if !h.checkStepUpCode(w, r, p.UserID, in.Code) {
		return
	}
	if err := h.repo.DisableTOTP(r.Context(), p.UserID); err != nil {
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
	if !h.verifyPasswordOr401(w, r, p.UserID, in.Password) {
		return
	}
	if !h.checkStepUpCode(w, r, p.UserID, in.Code) {
		return
	}
	codes, err := h.mintRecoveryCodes(r.Context(), p.UserID)
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

// checkStepUpCode verifies a current TOTP code under an attempt budget, writing
// the refusal itself.
//
// The budget is what separates these endpoints from Verify2FA, which is capped
// by auth_challenge.attempts. There is no challenge on a session-authenticated
// step-up, so without a limiter an attacker holding a hijacked session could
// grind the ~3 codes valid at any instant out of a space of a million.
func (h *Handler) checkStepUpCode(w http.ResponseWriter, r *http.Request, uid authctx.UserID, code string) bool {
	key := "stepup:" + strconv.FormatInt(int64(uid), 10)
	until, ok := h.stepUpUser.Begin(key)
	if !ok {
		writeRateLimited(w, until)
		return false
	}
	if !h.checkTOTP(r.Context(), uid, normalizeOTPCode(code)) {
		h.stepUpUser.CommitFail(key)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_code", "that code is not valid"))
		return false
	}
	h.stepUpUser.CommitSuccess(key)
	return true
}

// requireChallenge resolves the pre-auth cookie or writes the refusal.
func (h *Handler) requireChallenge(w http.ResponseWriter, r *http.Request, purposes ...string) (Challenge, bool) {
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
			"too many incorrect codes; sign in again"))
	case errors.Is(err, ErrChallengeInvalid):
		h.cookies.ClearPreAuth(w)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "challenge_invalid",
			"this sign-in attempt expired; start again"))
	default:
		h.logger.Error("challenge", "err", err)
		httperr.Write(w, httperr.ErrInternal)
	}
}

// enrollmentPrincipal resolves who is enrolling, demanding a password from a
// session-authenticated caller.
//
// Two callers reach the enrollment endpoints: a signed-in user adding a factor
// from settings, and an admin diverted into mandatory enrollment mid-login. The
// second has no session yet and has already proven the password; the first has
// a session and must prove it again.
func (h *Handler) enrollmentPrincipal(w http.ResponseWriter, r *http.Request) (authctx.UserID, bool) {
	if p, ok := authctx.FromContext(r.Context()); ok {
		in, err := httperr.DecodeJSON[totpStartInput](w, r)
		if err != nil {
			httperr.Write(w, err)
			return 0, false
		}
		if !h.verifyPasswordOr401(w, r, p.UserID, in.Password) {
			return 0, false
		}
		return p.UserID, true
	}
	ch, ok := h.requireChallenge(w, r, PurposeEnroll2FA)
	if !ok {
		return 0, false
	}
	return ch.UserID, true
}

// enrollmentPrincipalNoPassword is the same resolution for the endpoints that
// act on an ALREADY-CREATED pending enrollment (the QR image and the confirm
// step), where the password was proven by the call that created it.
func (h *Handler) enrollmentPrincipalNoPassword(w http.ResponseWriter, r *http.Request) (authctx.UserID, bool) {
	if p, ok := authctx.FromContext(r.Context()); ok {
		return p.UserID, true
	}
	ch, ok := h.requireChallenge(w, r, PurposeEnroll2FA)
	if !ok {
		return 0, false
	}
	return ch.UserID, true
}

func (h *Handler) verifyPasswordOr401(w http.ResponseWriter, r *http.Request, uid authctx.UserID, password string) bool {
	switch err := h.repo.VerifyUserPassword(r.Context(), uid, password); {
	case err == nil:
		return true
	case errors.Is(err, ErrBadCredentials), errors.Is(err, ErrPasswordMissing):
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
			"password is incorrect"))
		return false
	default:
		h.logger.Error("verify password", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return false
	}
}

// mintRecoveryCodes generates, stores and returns a fresh set.
func (h *Handler) mintRecoveryCodes(ctx context.Context, uid authctx.UserID) ([]string, error) {
	codes, err := newRecoveryCodes(recoveryCodeCount)
	if err != nil {
		return nil, err
	}
	hashes := make([][]byte, 0, len(codes))
	for _, c := range codes {
		// Hash the NORMALIZED form, because verification normalizes too. Storing
		// the display form with its hyphen would make every typed code fail.
		hashes = append(hashes, secrets.Hash(normalizeRecoveryCode(c)))
	}
	if err := h.repo.ReplaceRecoveryCodes(ctx, uid, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

func (h *Handler) notifyRecoveryCodeUsed(u User) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		remaining, err := h.repo.CountRecoveryCodes(ctx, u.ID)
		if err != nil {
			return
		}
		if err := h.mailer.Send(ctx, mailer.RecoveryCodeUsedMessage(u.Email, remaining)); err != nil {
			h.logger.Error("recovery code notification", "err", err)
		}
	}()
}

// sendAsync delivers a message on a detached context.
//
// The request must not wait on SMTP — a slow or dead mail server would turn
// every code request into a timeout — and the request's own context is
// cancelled the moment the handler returns, so it cannot be reused here.
func (h *Handler) sendAsync(msg mailer.Message, what string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := h.mailer.Send(ctx, msg); err != nil {
			h.logger.Error("send mail", "what", what, "err", err)
		}
	}()
}

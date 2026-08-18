package auth

import (
	"errors"
	"net/http"
	"strconv"

	"foldex/internal/mailer"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/secrets"
)

// The e-mail second factor (ADR-37).
//
// Mirrors /2fa/totp/{start,confirm,disable} deliberately: the epoch binding, the
// confirmation CAS under the app_user lock and the mandatory recovery codes are
// the same mechanisms, and reimplementing them differently here would mean two
// enrollment flows whose security properties have to be re-derived separately.
//
// The one difference that matters is WHY recovery codes are mandatory. For TOTP
// they are a convenience against a lost phone. Here they are the only exit from
// a lockout the design creates on purpose: an account whose sole factor is
// e-mail, arriving through a password-reset link, carries
// `mailbox_already_proven` and is refused the e-mail method — because otherwise
// one mailbox would satisfy both steps. Without recovery codes that safety rule
// becomes a locked door.

// StartEmailFactor opens an enrollment and mails the code that confirms it.
func (h *Handler) StartEmailFactor(w http.ResponseWriter, r *http.Request) {
	if h.codeMAC == nil {
		httperr.Write(w, httperr.New(http.StatusNotImplemented, "2fa_unavailable",
			"two-factor authentication is not configured on this instance"))
		return
	}
	// The `log` driver prints the body to stdout. Enrolling against it would
	// install a "factor" whose codes anyone with the container logs can read —
	// the same reason it is refused as a login method.
	if h.mailer.Driver() != "smtp" {
		httperr.Write(w, httperr.New(http.StatusConflict, "email_factor_unavailable",
			"this instance has no SMTP delivery configured"))
		return
	}
	uid, tokenVersion, sessionID, ok := h.enrollmentPrincipal(w, r)
	if !ok {
		return
	}
	user, err := h.repo.GetUser(r.Context(), uid)
	if err != nil {
		h.logger.Error("email factor start user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	code, err := secrets.NewNumericCode(totpDigits)
	if err != nil {
		h.logger.Error("email factor code", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	ttl := h.otpTTL(r.Context())
	email := user.Email
	draft := MailDraft{Locale: localeFor(user.Locale, r), Build: func(string) mailer.Envelope {
		// The MAILED lifetime is the one just persisted, not a constant.
		return mailer.EnrollEmail2FAMessage(email, code, int(ttl.Minutes()))
	}}
	// The digest is bound to (user, purpose) with no challenge id: enrollment
	// from Settings has no challenge, and binding to one would make the
	// pre-auth and session paths need different digests for the same code.
	err = h.repo.StartEmailFactorEnrollment(r.Context(), uid, tokenVersion, sessionID,
		h.codeMAC.EmailOTPDigest(uid, OTPPurposeEnrollEmail2FA, nil, code),
		ttl, h.otpCooldown(r.Context()), draft)
	switch {
	case err == nil:
	case errors.Is(err, ErrFactorAlreadyConfirmed):
		httperr.Write(w, httperr.New(http.StatusConflict, "email_factor_already_enabled",
			"e-mail is already enrolled as a second factor; disable it first"))
		return
	case errors.Is(err, ErrTooSoon):
		// 202, not 429: the caller is authenticated and this is their own
		// enrollment, so there is no enumeration to protect — but repeating the
		// cooldown as an error would invite a retry loop on a code already sent.
		w.WriteHeader(http.StatusAccepted)
		return
	case errors.Is(err, ErrChallengeInvalid):
		h.writeChallengeError(w, err)
		return
	case errors.Is(err, ErrSessionInvalid):
		h.writeSessionInvalid(w)
		return
	default:
		h.logger.Error("email factor start", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{
		"account":    MaskEmail(user.Email),
		"expires_in": int(ttl.Seconds()),
		"digits":     totpDigits,
	})
}

type emailFactorConfirmInput struct {
	Code string `json:"code"`
}

// ConfirmEmailFactor activates the enrollment and returns the recovery codes.
//
// Shown EXACTLY once: the server keeps only their keyed MAC, so it genuinely
// cannot show them again.
func (h *Handler) ConfirmEmailFactor(w http.ResponseWriter, r *http.Request) {
	if h.codeMAC == nil {
		httperr.Write(w, httperr.New(http.StatusNotImplemented, "2fa_unavailable",
			"two-factor authentication is not configured on this instance"))
		return
	}
	uid, tokenVersion, sessionID, ok := h.enrollmentPrincipalNoPassword(w, r)
	if !ok {
		return
	}
	in, err := httperr.DecodeJSON[emailFactorConfirmInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	// The confirming code is GUESSABLE, and that is what separates this endpoint
	// from its TOTP twin. Confirming a TOTP enrollment needs a code derived from
	// the seed the same caller was just handed, so there is nothing to brute
	// force. Here the code goes to the account's mailbox and never to the
	// caller — so on the pre-auth path an attacker who knows only the password
	// could start an enrollment against the victim's address and then grind six
	// digits until one confirms, and confirming ISSUES THE SESSION.
	//
	// Charged BEFORE the digest is compared, for the same reason Verify2FA
	// charges first: an attempt that costs nothing when cancelled mid-flight
	// turns a budget of five into no budget at all.
	var challenge *Challenge
	var enrollKey string
	// Settled by DEFER rather than at each return. attemptlimit.Begin reserves a
	// slot that exactly one of CommitFail/CommitSuccess/Release must return, and
	// Sweep skips in-flight entries — so a single unsettled exit path drifts the
	// key toward a lockout nothing earned. Settling inline works only while
	// every future return remembers to; a defer covers the ones not written yet.
	//
	// `settleErr` starts as a sentinel that matches no branch of settleStepUp,
	// whose default is Release: a failure before the caller's code was examined
	// must give the slot back WITHOUT charging a guess nobody made.
	settleErr := errProofNotAttempted
	defer func() {
		if enrollKey != "" {
			h.settleStepUp(enrollKey, settleErr)
		}
	}()
	if _, authenticated := authctx.FromContext(r.Context()); !authenticated {
		ch, err := h.repo.ResolveChallenge(r.Context(), cookieValue(r, CookiePreAuth), PurposeEnroll2FA)
		if err != nil {
			h.writeChallengeError(w, err)
			return
		}
		// The cap lives in BumpChallengeAttempt's own UPDATE (`attempts < max`),
		// which answers ErrChallengeExhausted rather than incrementing past it;
		// writeChallengeError turns that into 429 and clears the pre-auth
		// cookie, leaving the row live until its window ends so another correct
		// password cannot mint a fresh set of guesses.
		if _, err := h.repo.BumpChallengeAttempt(r.Context(), ch.ID); err != nil {
			h.writeChallengeError(w, err)
			return
		}
		challenge = &ch
	} else {
		// The session path has no challenge to carry a budget, so it uses the
		// in-memory per-user limiter — the same reason the step-up paths do.
		// A separate key from "stepup:": a wrong enrollment code should not
		// spend the budget that guards disabling a factor.
		key := "enroll:" + strconv.FormatInt(int64(uid), 10)
		until, ok := h.stepUpUser.Begin(key)
		if !ok {
			writeRateLimited(w, until)
			return
		}
		enrollKey = key
	}

	codes, hashes, err := h.newRecoveryCodeSet(uid)
	if err != nil {
		h.logger.Error("recovery codes", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	user, tok, err := h.repo.CompleteEmailFactorEnrollment(r.Context(), uid, tokenVersion,
		h.codeMAC.EmailOTPDigest(uid, OTPPurposeEnrollEmail2FA, nil, normalizeOTPCode(in.Code)),
		hashes, sessionID, challenge, h.ttl, clientIP(r), r.UserAgent())
	settleErr = err
	switch {
	case err == nil:
	case errors.Is(err, ErrBadCredentials):
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_code",
			"that code is not valid"))
		return
	case errors.Is(err, ErrNoPendingFactor):
		httperr.Write(w, httperr.New(http.StatusBadRequest, "no_enrollment",
			"start an enrollment first"))
		return
	case errors.Is(err, ErrChallengeInvalid):
		h.writeChallengeError(w, err)
		return
	case errors.Is(err, ErrSessionInvalid):
		h.writeSessionInvalid(w)
		return
	default:
		h.logger.Error("email factor confirm", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	// Enrolling from the pre-auth (mandatory admin enrollment) flow completes
	// the login: the password was proven to get the challenge, and the mailbox
	// has just been proven too.
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

// SendStepUpEmailOTP mails a code that an ENROLLED e-mail factor can present to
// authorize a credential change from a live session.
//
// Without this endpoint the e-mail factor is a second-class credential: it can
// sign you in, but every session-authenticated step-up (disable a factor,
// regenerate recovery codes, set a password, link an identity) would only ever
// accept an authenticator code or a recovery code. An account whose ONLY factor
// is e-mail would have to spend one of its finite recovery codes to turn that
// factor off — a lockout budget consumed by an ordinary settings change.
//
// `mailbox_already_proven` has no analogue here and needs none: that flag exists
// because a password-reset link and a mailed code arrive on the same channel,
// and this path has no reset. A session only exists because a full login already
// satisfied both factors.
func (h *Handler) SendStepUpEmailOTP(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	if h.codeMAC == nil || h.mailer.Driver() != "smtp" {
		httperr.Write(w, httperr.New(http.StatusConflict, "email_factor_unavailable",
			"this instance has no SMTP delivery configured"))
		return
	}
	user, err := h.repo.GetUser(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("step-up otp user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// Refused rather than silently accepted: the caller is authenticated and
	// asking about their own account, so there is nothing to conceal, and a 202
	// would leave them waiting for a code that was never going to arrive.
	if !user.Email2FAEnabled {
		httperr.Write(w, httperr.New(http.StatusConflict, "email_factor_not_enabled",
			"e-mail is not enrolled as a second factor"))
		return
	}
	code, err := secrets.NewNumericCode(totpDigits)
	if err != nil {
		h.logger.Error("step-up otp generate", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	ttl := h.otpTTL(r.Context())
	email := user.Email
	draft := MailDraft{Locale: localeFor(user.Locale, r), Build: func(string) mailer.Envelope {
		return mailer.StepUpCodeMessage(email, code, int(ttl.Minutes()))
	}}
	err = h.repo.CreateStepUpEmailOTP(r.Context(), p.UserID, p.SessionID, user.TokenVersion,
		h.codeMAC.EmailOTPDigest(p.UserID, OTPPurposeStepUp2FA, nil, code),
		ttl, h.otpCooldown(r.Context()), draft)
	switch {
	case err == nil, errors.Is(err, ErrTooSoon):
		// The cooldown answers 202 like a success: a code is already in flight,
		// and reporting that as an error invites a retry loop against a message
		// the user simply has not read yet.
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, ErrNoPendingFactor):
		httperr.Write(w, httperr.New(http.StatusConflict, "email_factor_not_enabled",
			"e-mail is not enrolled as a second factor"))
	case errors.Is(err, ErrChallengeInvalid):
		h.writeChallengeError(w, err)
	case errors.Is(err, ErrSessionInvalid):
		h.writeSessionInvalid(w)
	default:
		h.logger.Error("step-up otp", "err", err)
		httperr.Write(w, httperr.ErrInternal)
	}
}

type emailFactorDisableInput struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// DisableEmailFactor removes the factor. Requires BOTH the password and a
// current second-factor proof — the same two the enrollment demanded, so a
// stolen session alone cannot strip protection it cannot satisfy.
func (h *Handler) DisableEmailFactor(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	in, err := httperr.DecodeJSON[emailFactorDisableInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	tokenVersion, err := h.repo.VerifyUserPasswordEpoch(r.Context(), p.UserID, in.Password)
	if err != nil {
		if errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrPasswordMissing) {
			httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
				"password is incorrect"))
			return
		}
		h.logger.Error("email factor disable password", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// An admin under a mandatory-2FA policy may not remove their last factor:
	// they would be diverted straight back into enrollment on the next request,
	// having lost the one they had.
	user, err := h.repo.GetUser(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("email factor disable user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if !h.mayRemoveFactor(h.mw.requireTOTPForAdmins(r.Context()), user, factorEmail) {
		// Same rule as DisableTOTP's, so the same answer: a 409 here and a 403
		// there for one refusal would read as two different policies, and
		// `admin_2fa_required` is what the middleware emits for a DIFFERENT
		// condition (an admin who has no factor at all).
		httperr.Write(w, httperr.New(http.StatusForbidden, "totp_required_for_admins",
			"administrators must keep two-factor authentication enabled"))
		return
	}
	proof, key, ok := h.stepUpSecondFactor(w, r, p.UserID, user, in.Code)
	if !ok {
		return
	}
	// Deferred for the same reason as ConfirmEmailFactor above, and the key is
	// SHARED with every other step-up, so one unsettled exit here locks the
	// account out of disabling TOTP, regenerating codes and setting a password
	// too.
	disableErr := errProofNotAttempted
	defer func() { h.settleStepUp(key, disableErr) }()
	err = h.repo.DisableEmailFactor(r.Context(), p.UserID, p.SessionID, tokenVersion, proof)
	disableErr = err
	if err != nil {
		switch {
		// The proof is verified before the transaction and SPENT inside it, so
		// the window between them can legitimately close: a TOTP code replayed
		// by a racing request, a recovery code another tab just burned, a mailed
		// code that expired. All three are the caller's problem, not the
		// server's — reporting them as 500 both lies about whose fault it is and
		// files an ERROR log line for an ordinary refusal. DisableTOTP has
		// mapped ErrTOTPReplay this way since it was written.
		case errors.Is(err, ErrTOTPReplay), errors.Is(err, ErrBadCredentials):
			httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_code",
				"that code is not valid"))
		case errors.Is(err, ErrNoPendingFactor):
			httperr.Write(w, httperr.New(http.StatusConflict, "email_factor_not_enabled",
				"e-mail is not enrolled as a second factor"))
		case errors.Is(err, ErrChallengeInvalid):
			h.writeChallengeError(w, err)
		case errors.Is(err, ErrSessionInvalid):
			h.writeSessionInvalid(w)
		default:
			h.logger.Error("email factor disable", "err", err)
			httperr.Write(w, httperr.ErrInternal)
		}
		return
	}
	h.notifyIfRecovery(r, user, proof)
	w.WriteHeader(http.StatusNoContent)
}

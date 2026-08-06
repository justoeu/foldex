package auth

import (
	"errors"
	"net/http"
	"time"

	"foldex/internal/mailer"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/secrets"
)

// passwordResetTTL is deliberately short. The link is a full account takeover
// in one click if intercepted, and a user who asked for it is, by definition,
// at their keyboard right now.
const passwordResetTTL = 30 * time.Minute

type forgotPasswordInput struct {
	Email string `json:"email"`
}

// ForgotPassword ALWAYS answers 202.
//
// Not "usually" — always, including for an unknown address, a disabled account,
// a Google-only account and a request refused by the rate limiter. The endpoint
// is otherwise a perfect account-existence oracle, and unlike login there is no
// password to slow an attacker down: they would simply enumerate.
//
// The uniformity has to hold on THREE channels, and each one broke a naive
// implementation somewhere:
//
//  1. the status code and body — one shape for every outcome;
//  2. the timing — the work differs enormously between "no row found" and
//     "insert a token", so the same duration floor the login path uses applies;
//  3. the INBOX — a Google-only account that silently received nothing would
//     still be distinguishable to anyone who owns the address, so it gets a
//     "this account signs in with Google" message instead of a reset link.
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	defer floorDuration(time.Now(), loginFloor)

	in, err := httperr.DecodeJSON[forgotPasswordInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// Accepted before validation, too: a 400 for a malformed address would tell
	// an attacker their probe was at least well-formed, which is one bit more
	// than they should get from an endpoint that answers unconditionally.
	if validateEmail(in.Email) != nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ip := clientIP(r)
	ipKey := "pwreset:ip:" + ip
	emailKey := "pwreset:em:" + NormalizeEmail(in.Email)
	if _, ok := h.pwResetIP.Begin(ipKey); !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if _, ok := h.pwResetEmail.Begin(emailKey); !ok {
		h.pwResetIP.Release(ipKey)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// Every reset REQUEST is charged, successful or not. The budget here caps
	// how many e-mails one address can be made to receive, so only counting
	// "real" ones would leave the mailbombing case uncapped.
	defer h.pwResetIP.CommitFail(ipKey)
	defer h.pwResetEmail.CommitFail(emailKey)

	user, eligible, err := h.repo.UserForPasswordReset(r.Context(), in.Email)
	if err != nil {
		h.logger.Error("forgot password lookup", "err", err)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch {
	case !eligible && user.ID != 0 && user.Status == StatusActive && !user.HasPassword:
		// The account exists and is active but has no password — Google-only
		// (ADR-31). Tell the owner how to get in; deliberately WITHOUT a reset
		// link, since a link here would let control of the mailbox alone
		// resurrect a password credential, which is precisely what requiring
		// the current password during conversion refused to allow.
		h.sendAsync(mailer.PasswordResetUnavailableMessage(user.Email), "password reset unavailable")
	case eligible:
		token, terr := h.repo.CreatePasswordReset(r.Context(), user.ID, passwordResetTTL, ip)
		if terr != nil {
			h.logger.Error("create password reset", "err", terr)
			break
		}
		h.sendAsync(mailer.PasswordResetMessage(user.Email,
			h.baseURL+"/?reset="+token, int(passwordResetTTL.Minutes())), "password reset")
	}
	w.WriteHeader(http.StatusAccepted)
}

type resetPasswordInput struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// ResetPassword consumes a reset token and signs the user in.
//
// Signing them in is a deliberate choice: they have just proven control of the
// mailbox AND chosen a new password, which is strictly more than the login
// screen asks for. Bouncing them to a login form to retype what they typed
// thirty seconds ago adds no security and loses people.
//
// A second factor still applies. The reset proves the FIRST factor only, so an
// account with an authenticator diverts into the same challenge login uses —
// otherwise a compromised mailbox would bypass 2FA entirely.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	key := "pwreset:consume:" + ip
	if until, ok := h.pwResetIP.Begin(key); !ok {
		writeRateLimited(w, until)
		return
	}
	settled := false
	defer func() {
		if !settled {
			h.pwResetIP.Release(key)
		}
	}()

	in, err := httperr.DecodeJSON[resetPasswordInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := validatePassword(in.Password); err != nil {
		httperr.Write(w, err)
		return
	}

	user, err := h.repo.ConsumePasswordReset(r.Context(), in.Token, in.Password)
	switch {
	case errors.Is(err, ErrResetInvalid):
		h.pwResetIP.CommitFail(key)
		settled = true
		httperr.Write(w, httperr.New(http.StatusNotFound, "reset_invalid",
			"this reset link is no longer valid"))
		return
	case err != nil:
		h.logger.Error("reset password", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.pwResetIP.CommitSuccess(key)
	settled = true

	// mailboxAlreadyProven: the caller reached this endpoint by reading a link
	// sent to the account's address, so the mailbox cannot also serve as the
	// second factor.
	h.completeLogin(w, r, user, true)
}

// ─────────────────────────────────────────────────────────────────────
// E-mail verification
// ─────────────────────────────────────────────────────────────────────

// emailVerifyTTL matches the login OTP: long enough to switch to a mail client,
// short enough that a code left in an inbox stops working.
const emailVerifyTTL = emailOTPTTL

type verifyEmailInput struct {
	Token string `json:"token"`
}

// SendEmailVerification mails a confirmation code to the caller's own address.
//
// Session-authenticated, so there is no address to enumerate: the recipient is
// whoever is already signed in, never a value from the request. Accepting a
// target address here would turn an authenticated account into a mail relay.
func (h *Handler) SendEmailVerification(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	user, err := h.repo.GetUser(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("verify email load user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if user.EmailVerifiedAt != nil {
		// Already done. 204 rather than an error: the caller asked for a state
		// that already holds, which is success by any useful definition.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// A LINK, not a six-digit code. Confirming an address is a one-click action
	// the user performs from their inbox; a code would force them to switch
	// back to the app and retype it for no gain. The token is 256 bits from
	// crypto/rand, which is what lets the endpoint that consumes it work with
	// no session at all.
	token, hash, err := secrets.NewToken()
	if err != nil {
		h.logger.Error("verify email token", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if err := h.repo.CreateEmailOTP(r.Context(), user.ID, nil, OTPPurposeVerifyEmail,
		hash, emailVerifyTTL); err != nil {
		h.logger.Error("verify email store", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.sendAsync(mailer.VerifyEmailMessage(user.Email,
		h.baseURL+"/?verify="+token, int(emailVerifyTTL.Minutes())), "verify email")
	w.WriteHeader(http.StatusAccepted)
}

// VerifyEmail consumes a confirmation token and marks the address proven.
//
// UNAUTHENTICATED by design: the link is followed from a mail client, and
// requiring a session first would mean the common case — a new user who has
// not signed in on this device — is met with a login form and a token that
// expires while they find their password. The token itself is the credential.
//
// It answers 204 on success and one 404 for every failure — unknown, expired,
// already spent. Distinguishing them would let an unauthenticated caller probe
// which tokens ever existed.
func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[verifyEmailInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if in.Token == "" {
		httperr.Write(w, errVerifyInvalid())
		return
	}
	// One statement spends the token AND records the result — see the
	// repository method for why they cannot be two.
	if _, err := h.repo.ConsumeEmailVerification(r.Context(), secrets.Hash(in.Token)); err != nil {
		if errors.Is(err, ErrBadCredentials) {
			httperr.Write(w, errVerifyInvalid())
			return
		}
		h.logger.Error("verify email consume", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func errVerifyInvalid() error {
	return httperr.New(http.StatusNotFound, "verify_invalid",
		"this confirmation link is no longer valid")
}

package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"foldex/internal/mailer"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/secrets"
)

type requestEmailChangeInput struct {
	NewEmail string `json:"new_email"`
	// Password is the step-up. The current password, not the second factor:
	// the same reasoning as changing a password while signed in — demanding a
	// factor as well would lock out an owner who knows their password and
	// cannot reach their authenticator, and the change cannot complete without
	// control of the destination mailbox anyway.
	Password string `json:"password"`
}

// RequestEmailChange starts a move to a new address. It never moves it.
//
// The address only changes when the link mailed to the NEW mailbox is opened,
// which is what makes a typo harmless: an address nobody controls simply never
// confirms, and the account keeps working. Writing it straight in would make
// the mistyped address the login AND the recovery channel, with the warning
// going to the address that was typed wrong.
//
// The CURRENT address gets a linkless notice at the same time. That message is
// the only channel left to someone whose account is being taken over by a
// person who already has their session.
func (h *Handler) RequestEmailChange(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[requestEmailChangeInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	p, _ := authctx.FromContext(r.Context())

	// TRIMMED, not normalized. The repository folds its own copy for the lookup
	// column; handing it an already-lowercased value makes `new_email` and
	// `new_email_normalized` identical, and `Jane.Smith@Company.COM` reaches
	// `app_user.email` as `jane.smith@company.com` — silently, permanently, and
	// contrary to how every other write to that column behaves.
	email := strings.TrimSpace(in.NewEmail)
	// The SAME validator registration and invitations use, and reusing it is the
	// point: a second implementation dropped the length bound, so a 400-character
	// address passed here, was written to email_change, and only failed at the
	// CHECK when the link was clicked — as an opaque 500, to an unauthenticated
	// caller, with no way to correct it.
	if err := validateEmail(email); err != nil {
		httperr.Write(w, err)
		return
	}

	// The same per-user password budget the OAuth link step-up uses. Without
	// it, a stolen session is an unlimited password oracle on a form that
	// happens to be about e-mail.
	passwordKey := "stepup-password:" + strconv.FormatInt(int64(p.UserID), 10)
	if until, ok := h.stepUpPasswordUser.Begin(passwordKey); !ok {
		writeRateLimited(w, until)
		return
	}
	if _, err := h.repo.VerifyUserPasswordEpoch(r.Context(), p.UserID, in.Password); err != nil {
		switch {
		case errors.Is(err, ErrBadCredentials), errors.Is(err, ErrPasswordMissing):
			h.stepUpPasswordUser.CommitFail(passwordKey)
			httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
				"current password is incorrect"))
		default:
			h.stepUpPasswordUser.Release(passwordKey)
			h.logger.Error("email change password verification", "err", err)
			httperr.Write(w, httperr.ErrInternal)
		}
		return
	}
	h.stepUpPasswordUser.CommitSuccess(passwordKey)

	// The log driver prints the body to stdout, and this link MOVES the
	// account. Same refusal the second factor and administrator recovery make.
	if h.mailer.Driver() != "smtp" {
		httperr.Write(w, httperr.New(http.StatusServiceUnavailable, "mail_unavailable",
			"this instance cannot send e-mail, so the address cannot be changed here"))
		return
	}

	ttl := h.otpTTL(r.Context())
	pending, err := h.repo.RequestEmailChange(r.Context(), p.UserID, int64(p.SessionID), email, ttl,
		func(storedLocale string) MailDraft {
			return MailDraft{Locale: localeFor(storedLocale, r), Build: func(token string) mailer.Envelope {
				return mailer.EmailChangeConfirmMessage(email,
					h.baseURL+"/#email-change="+token, int(ttl.Minutes()))
			}}
		},
		func(oldEmail, storedLocale string) MailDraft {
			return MailDraft{Locale: localeFor(storedLocale, r), Build: func(string) mailer.Envelope {
				return mailer.EmailChangeNoticeMessage(oldEmail, email)
			}}
		})
	switch {
	case errors.Is(err, ErrEmailTaken):
		// Told plainly, to an AUTHENTICATED caller who proved their password on
		// a rate-limited route. The alternative — one answer for taken and free
		// alike — would leave the user staring at a confirmation that never
		// arrives with nothing to act on. The enumeration this exposes is
		// bounded by a credential the attacker must already hold.
		httperr.Write(w, httperr.New(http.StatusConflict, "email_taken",
			"another account already uses that address"))
	case errors.Is(err, ErrEmailUnchanged):
		httperr.Write(w, httperr.New(http.StatusBadRequest, "email_unchanged",
			"that is already this account's address"))
	case errors.Is(err, ErrEmailChangeInvalid):
		httperr.Write(w, httperr.New(http.StatusForbidden, "email_change_forbidden",
			"this account cannot change its address"))
	case err != nil:
		h.logger.Error("email change request", "err", err)
		httperr.Write(w, httperr.ErrInternal)
	default:
		httperr.JSON(w, http.StatusAccepted, pending)
	}
}

// GetEmailChange reports the caller's live request, so the profile screen can
// say "we sent a link to X" after a reload instead of forgetting it happened.
//
// It answers 200 with `null` when there is none: an endpoint that 404s for the
// ordinary case makes every caller special-case a status code to learn nothing.
func (h *Handler) GetEmailChange(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	pending, err := h.repo.PendingEmailChangeFor(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("pending email change", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"pending": pending})
}

// CancelEmailChange drops the caller's pending request.
func (h *Handler) CancelEmailChange(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	if err := h.repo.CancelEmailChange(r.Context(), p.UserID); err != nil {
		h.logger.Error("cancel email change", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type confirmEmailChangeInput struct {
	Token string `json:"token"`
}

// ConfirmEmailChange consumes the token and moves the address.
//
// UNAUTHENTICATED, for the same reason `/email/verify` is: the link is opened
// from a mail client, and the mailbox it was sent to may well be read on a
// device that never signed in — which is the common case here, since the whole
// point is that the user is moving to a different address.
//
// Every failure is the same 404. Unknown, expired, already spent and
// epoch-stale are indistinguishable on purpose; telling them apart would let an
// unauthenticated caller probe which tokens ever existed.
func (h *Handler) ConfirmEmailChange(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[confirmEmailChangeInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if in.Token == "" {
		httperr.Write(w, errEmailChangeInvalid())
		return
	}
	user, err := h.repo.ConsumeEmailChange(r.Context(), secrets.Hash(in.Token))
	switch {
	case errors.Is(err, ErrEmailChangeInvalid):
		httperr.Write(w, errEmailChangeInvalid())
		return
	case errors.Is(err, ErrEmailTaken):
		// Claimed between the request and the click. A distinct answer here is
		// safe and necessary: the caller holds a token proving control of the
		// destination mailbox, and "invalid link" would send them to support
		// over a state they can actually resolve by choosing another address.
		httperr.Write(w, httperr.New(http.StatusConflict, "email_taken",
			"another account took that address before this link was opened"))
		return
	case err != nil:
		h.logger.Error("email change confirm", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	// Every session died with the identifier they were issued against, this
	// one included, so the cookies have to go too — leaving them would hand the
	// SPA an access token the next request rejects.
	h.cookies.ClearSession(w)
	if err := h.repo.Audit(r.Context(), AuditRecord{
		Action: AuditEmailChanged,
		// Actor and target are the same account: the person holding the link
		// proved control of the destination mailbox, and there is no
		// administrator in this path.
		ActorID:     &user.ID,
		TargetID:    &user.ID,
		TargetEmail: user.Email,
	}); err != nil {
		h.logger.Error("audit email change", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func errEmailChangeInvalid() error {
	return httperr.New(http.StatusNotFound, "email_change_invalid",
		"this confirmation link is no longer valid")
}

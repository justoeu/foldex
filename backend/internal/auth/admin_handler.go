package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/mailer"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// AdminHandler serves /api/admin/users. Every route here sits behind
// Authenticate + RequireAdmin; nothing in this file re-checks the role.
type AdminHandler struct {
	repo       *Repository
	mailer     mailer.Mailer
	dispatcher *mailer.Dispatcher
	logger     *slog.Logger
	baseURL    string
}

func NewAdminHandler(repo *Repository, m mailer.Mailer, dispatcher *mailer.Dispatcher,
	logger *slog.Logger, baseURL string) *AdminHandler {
	return &AdminHandler{
		repo: repo, mailer: m, dispatcher: dispatcher, logger: logger,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

func (h *AdminHandler) Mount(r chi.Router) {
	r.Use(NoStore)
	r.Get("/users", h.ListUsers)
	r.Patch("/users/{id}", h.UpdateUser)
	r.Delete("/users/{id}", h.DeleteUser)
	r.Post("/users/{id}/sessions/revoke", h.RevokeUserSessions)
	r.Post("/users/{id}/force-password-reset", h.ForcePasswordReset)
	r.Get("/invites", h.ListInvites)
	r.Post("/invites", h.CreateInvite)
	r.Delete("/invites/{id}", h.RevokeInvite)
}

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.repo.ListUsers(r.Context())
	if err != nil {
		h.logger.Error("admin list users", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"users": users})
}

type updateUserInput struct {
	Name   *string       `json:"name"`
	Role   *authctx.Role `json:"role"`
	Status *string       `json:"status"`
}

// UpdateUser edits an account's name, role or status.
//
// Two guards are enforced here and mirrored (never trusted) in the UI:
// an admin cannot demote or disable THEMSELVES, and the last active admin
// cannot be demoted or disabled by anyone. Both failure modes end in an
// instance with zero administrators, which no API call can undo — only a
// direct database edit.
func (h *AdminHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	caller, _ := authctx.FromContext(r.Context())
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in, err := httperr.DecodeJSON[updateUserInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// Owner is absent from the assignable set on purpose: ownership moves only
	// through the transfer endpoint, which demotes the outgoing owner in the
	// same statement. Allowing it here would let a promotion race the partial
	// unique index and surface as a 500 instead of a refusal.
	if in.Role != nil && (!in.Role.Valid() || *in.Role == authctx.RoleOwner) {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_role",
			"role must be admin, editor or viewer"))
		return
	}
	if in.Status != nil && *in.Status != StatusActive && *in.Status != StatusDisabled {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_status",
			"status must be active or disabled"))
		return
	}

	target := authctx.UserID(id)
	// What matters to the last-administrator guard is the loss of administrative
	// reach, not the exact role: admin -> editor and admin -> viewer are both
	// demotions, and with four roles a plain `!= RoleAdmin` would also count
	// admin -> owner, which adds one.
	demoting := in.Role != nil && !in.Role.IsAdmin()
	disabling := in.Status != nil && *in.Status == StatusDisabled

	if target == caller.UserID && (demoting || disabling) {
		httperr.Write(w, httperr.New(http.StatusConflict, "self_target",
			"you cannot demote or disable your own account"))
		return
	}
	// The last-admin guard is NOT checked here: it lives inside UpdateUser's
	// transaction, under an advisory lock, because a read-then-write check out
	// here lets two concurrent demotions both observe two admins and both
	// proceed — leaving zero, which no API call can undo.
	user, err := h.repo.UpdateUser(r.Context(), target, in.Name, in.Role, in.Status)
	switch {
	case errors.Is(err, ErrNoUser):
		httperr.Write(w, httperr.ErrNotFound)
		return
	case errors.Is(err, ErrLastAdmin):
		httperr.Write(w, errLastAdmin())
		return
	}
	if err != nil {
		h.logger.Error("admin update user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// A disabled account keeps no live sessions: leaving them alive would mean
	// the ban only takes effect when the current access token expires.
	if disabling {
		if err := h.repo.RevokeAllForUser(r.Context(), target, ReasonUserDisabled); err != nil {
			h.logger.Error("admin disable revoke", "err", err)
		}
	}
	httperr.JSON(w, http.StatusOK, user)
}

// DeleteUser removes an account and, by cascade, all of its content.
func (h *AdminHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	caller, _ := authctx.FromContext(r.Context())
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	target := authctx.UserID(id)
	if target == caller.UserID {
		httperr.Write(w, httperr.New(http.StatusConflict, "self_target",
			"you cannot delete your own account"))
		return
	}
	// Same as UpdateUser: the guard is inside DeleteUser's transaction.
	if err := h.repo.DeleteUser(r.Context(), target); err != nil {
		switch {
		case errors.Is(err, ErrNoUser):
			httperr.Write(w, httperr.ErrNotFound)
		case errors.Is(err, ErrLastAdmin):
			httperr.Write(w, errLastAdmin())
		default:
			h.logger.Error("admin delete user", "err", err)
			httperr.Write(w, httperr.ErrInternal)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandler) RevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if _, err := h.repo.GetUser(r.Context(), authctx.UserID(id)); err != nil {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	if err := h.repo.RevokeAllForUser(r.Context(), authctx.UserID(id), ReasonAdminRevoked); err != nil {
		h.logger.Error("admin revoke sessions", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ForcePasswordReset asks the target to recover through their verified mailbox.
// No credential changes and no secret is returned to the administrator. SMTP
// delivery and token publication are coupled by the repository transaction; the
// target later chooses their own password through the ordinary reset consumer.
func (h *AdminHandler) ForcePasswordReset(w http.ResponseWriter, r *http.Request) {
	caller, _ := authctx.FromContext(r.Context())
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	target := authctx.UserID(id)
	if target == caller.UserID {
		httperr.Write(w, httperr.New(http.StatusConflict, "self_target",
			"use the change-password form for your own account"))
		return
	}
	if h.mailer.Driver() != "smtp" {
		httperr.Write(w, httperr.New(http.StatusServiceUnavailable, "smtp_required",
			"administrator recovery requires SMTP delivery"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err = h.repo.CreateAdminPasswordRecovery(ctx, target, passwordResetTTL,
		func(email, token string) error {
			return h.mailer.Send(ctx, mailer.AdminPasswordRecoveryMessage(email,
				h.baseURL+"/#reset="+token, int(passwordResetTTL.Minutes())))
		})
	switch {
	case errors.Is(err, ErrNoUser):
		httperr.Write(w, httperr.ErrNotFound)
	case errors.Is(err, ErrRecoveryUnavailable):
		httperr.Write(w, httperr.New(http.StatusConflict, "recovery_unavailable",
			"the target must be active and have a verified e-mail address"))
	case errors.Is(err, ErrRecoveryDelivery):
		h.logger.Error("admin recovery mail", "err", err)
		httperr.Write(w, httperr.New(http.StatusServiceUnavailable, "mail_unavailable",
			"the recovery e-mail could not be delivered"))
	case err != nil:
		h.logger.Error("admin recovery", "err", err)
		httperr.Write(w, httperr.ErrInternal)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// errLastAdmin is the response for an operation the repository refused because
// it would have left the instance with no active administrator.
func errLastAdmin() error {
	return httperr.New(http.StatusConflict, "last_admin",
		"this is the last active administrator")
}

// ─────────────────────────────────────────────────────────────────────
// Invites
// ─────────────────────────────────────────────────────────────────────

func (h *AdminHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := h.repo.ListInvites(r.Context())
	if err != nil {
		h.logger.Error("admin list invites", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"invites": invites})
}

type createInviteInput struct {
	Email string       `json:"email"`
	Role  authctx.Role `json:"role"`
}

// CreateInvite issues an invitation and e-mails the link.
//
// The response carries accept_url — the ONLY time the raw token is ever
// visible, since the database holds only its sha256. That is deliberate: with
// the default `log` mail driver there is no inbox to check, and an admin who
// cannot copy the link has no way to invite anybody. A failed send is logged
// and the invite still returned, because the row is valid regardless of
// whether SMTP happened to be reachable.
func (h *AdminHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	caller, _ := authctx.FromContext(r.Context())
	in, err := httperr.DecodeJSON[createInviteInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := validateEmail(in.Email); err != nil {
		httperr.Write(w, err)
		return
	}
	role := in.Role
	if role == "" {
		role = authctx.RoleEditor
	}
	// Mirrors the invite_role_check constraint: an invitation can mint an
	// administrator but never an owner, so a leaked invite cannot hand someone
	// the one role that cannot be demoted.
	if !role.Valid() || role == authctx.RoleOwner {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_role",
			"role must be admin, editor or viewer"))
		return
	}

	inv, raw, err := h.repo.CreateInvite(r.Context(), in.Email, role, caller.UserID, inviteTTL)
	if errors.Is(err, ErrEmailTaken) {
		httperr.Write(w, httperr.New(http.StatusConflict, "email_taken", "e-mail already registered"))
		return
	}
	if err != nil {
		h.logger.Error("admin create invite", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	inv.AcceptURL = h.baseURL + "/#invite=" + raw

	inviter, _ := h.repo.GetUser(r.Context(), caller.UserID)
	msg := mailer.InviteMessage(inv.Email, inviter.Name, inv.AcceptURL, int(inviteTTL/time.Hour))
	if err := h.dispatcher.Enqueue(msg, "invite"); err != nil {
		h.logger.Warn("invite mail not queued", "err", err, "invite_id", inv.ID)
	}
	httperr.JSON(w, http.StatusCreated, inv)
}

func (h *AdminHandler) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := h.repo.RevokeInvite(r.Context(), id); err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

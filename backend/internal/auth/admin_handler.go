package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/mailer"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/pkg/httperr"
)

// errOwnerImmutable is the one refusal every people-management route shares:
// the owner's role and status move only through transfer.
func errOwnerImmutable() error {
	return httperr.New(http.StatusConflict, "owner_immutable",
		"the owner's role and status change only by transferring the instance")
}

// AdminHandler serves /api/admin/users. Every route here sits behind
// Authenticate + RequireAdmin; nothing in this file re-checks the role.
type AdminHandler struct {
	repo    *Repository
	mailer  mailer.Mailer
	logger  *slog.Logger
	baseURL string
}

func NewAdminHandler(repo *Repository, m mailer.Mailer,
	logger *slog.Logger, baseURL string) *AdminHandler {
	return &AdminHandler{
		repo: repo, mailer: m, logger: logger,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

func (h *AdminHandler) Mount(r chi.Router) {
	r.Use(NoStore)
	// Every route names the permission it needs, even where owner and admin
	// currently hold identical sets. The administration screen renders the
	// matrix to administrators as the enforced contract, so an entry nothing
	// gates would make that screen describe a rule the server does not apply —
	// and the day a role is added or narrowed, these mounts are what make the
	// difference real instead of theoretical.
	readUsers := authgate.RequirePermission(authctx.PermUsersRead)
	writeUsers := authgate.RequirePermission(authctx.PermUsersWrite)
	assignRoles := authgate.RequirePermission(authctx.PermRolesAssign)

	r.With(readUsers).Get("/users", h.ListUsers)
	// Role and status travel in the same PATCH, so the stricter of the two
	// permissions gates it.
	r.With(assignRoles).Patch("/users/{id}", h.UpdateUser)
	r.With(writeUsers).Delete("/users/{id}", h.DeleteUser)
	r.With(writeUsers).Post("/users/{id}/sessions/revoke", h.RevokeUserSessions)
	r.With(writeUsers).Post("/users/{id}/force-password-reset", h.ForcePasswordReset)
	// The only route in this file that is not open to every administrator. The
	// group gate already established that the caller administers; this one asks
	// the further question of whether they own the instance.
	r.With(authgate.RequirePermission(authctx.PermInstanceTransfer)).
		Post("/users/{id}/transfer-ownership", h.TransferOwnership)
	r.Get("/metrics", h.Metrics)
	r.Get("/roles", h.Roles)
	r.With(authgate.RequirePermission(authctx.PermAuditRead)).Get("/audit", h.ListAudit)
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
	case errors.Is(err, ErrOwnerImmutable):
		httperr.Write(w, errOwnerImmutable())
		return
	}
	if err != nil {
		h.logger.Error("admin update user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if in.Role != nil {
		h.audit(r, AuditRoleChanged, &user, string(*in.Role))
	}
	if in.Status != nil {
		h.audit(r, AuditStatusChanged, &user, *in.Status)
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
	// Read the target BEFORE deleting it: the trail denormalizes the e-mail, and
	// after the row is gone the id alone identifies nobody.
	//
	// The id is then dropped on purpose. audit_log.target_id is a foreign key to
	// app_user, and by the time the entry is written that row no longer exists —
	// keeping it would violate the constraint, and since an audit failure is
	// logged rather than propagated, the deletion would go unrecorded. The
	// denormalized e-mail is exactly what makes dropping it harmless.
	var deleted *User
	if u, err := h.repo.GetUser(r.Context(), target); err == nil {
		deleted = &User{Email: u.Email, Name: u.Name}
	}
	// Same as UpdateUser: the guard is inside DeleteUser's transaction.
	if err := h.repo.DeleteUser(r.Context(), target); err != nil {
		switch {
		case errors.Is(err, ErrNoUser):
			httperr.Write(w, httperr.ErrNotFound)
		case errors.Is(err, ErrLastAdmin):
			httperr.Write(w, errLastAdmin())
		case errors.Is(err, ErrOwnerImmutable):
			httperr.Write(w, errOwnerImmutable())
		default:
			h.logger.Error("admin delete user", "err", err)
			httperr.Write(w, httperr.ErrInternal)
		}
		return
	}
	h.audit(r, AuditUserDeleted, deleted, "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandler) RevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	target, err := h.repo.GetUser(r.Context(), authctx.UserID(id))
	if err != nil {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	if err := h.repo.RevokeAllForUser(r.Context(), authctx.UserID(id), ReasonAdminRevoked); err != nil {
		h.logger.Error("admin revoke sessions", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.audit(r, AuditSessionsRevoked, &target, "")
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
	locale := localeFrom(r)
	err = h.repo.CreateAdminPasswordRecovery(ctx, target, passwordResetTTL,
		func(email, token string) error {
			// Deliberately NOT the outbox. This is the one path that stays
			// synchronous inside its transaction: SMTP refusing the message rolls
			// the token back, so an administrator can never leave a live recovery
			// credential behind for a mailbox that never received it. Moving it to
			// the outbox would trade that for eventual delivery — a defensible
			// trade, but a change to a security invariant, not a refactor.
			msg, rerr := mailer.Render(mailer.AdminPasswordRecoveryMessage(email,
				h.baseURL+"/#reset="+token, int(passwordResetTTL.Minutes())), locale)
			if rerr != nil {
				return rerr
			}
			return h.mailer.Send(ctx, msg)
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
		// The target is read for the trail only; the recovery itself already
		// resolved it. A failed read still leaves an entry worth having.
		var recovered *User
		if u, err := h.repo.GetUser(r.Context(), target); err == nil {
			recovered = &u
		}
		h.audit(r, AuditPasswordRecovery, recovered, "")
		w.WriteHeader(http.StatusAccepted)
	}
}

// errLastAdmin is the response for an operation the repository refused because
// it would have left the instance with no active administrator.
func errLastAdmin() error {
	return httperr.New(http.StatusConflict, "last_admin",
		"this is the last active administrator")
}

// TransferOwnership hands the instance to another active account and demotes
// the caller to admin.
//
// The outgoing owner is always the CALLER, never a path parameter: an endpoint
// that took both ends would let one owner be replaced by a request that names
// them, and the only principal entitled to give the seat away is the one
// sitting in it. Both accounts lose their sessions, so the change cannot be
// half-applied to a browser still holding the old role.
func (h *AdminHandler) TransferOwnership(w http.ResponseWriter, r *http.Request) {
	caller, _ := authctx.FromContext(r.Context())
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	user, err := h.repo.TransferOwnership(r.Context(), caller.UserID, authctx.UserID(id))
	switch {
	case errors.Is(err, ErrNoUser):
		httperr.Write(w, httperr.ErrNotFound)
		return
	case errors.Is(err, ErrSelfTarget):
		httperr.Write(w, httperr.New(http.StatusConflict, "self_target",
			"you already own this instance"))
		return
	case errors.Is(err, ErrOwnerImmutable):
		// The caller passed the permission gate but no longer holds the seat —
		// a concurrent transfer moved it between the two.
		httperr.Write(w, httperr.New(http.StatusConflict, "not_owner",
			"only the current owner can transfer the instance"))
		return
	case errors.Is(err, ErrNotTransferable):
		httperr.Write(w, httperr.New(http.StatusConflict, "target_not_active",
			"ownership can only pass to an active account"))
		return
	}
	if err != nil {
		h.logger.Error("transfer ownership", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.audit(r, AuditOwnershipMoved, &user, "")
	httperr.JSON(w, http.StatusOK, user)
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

// audit appends one trail entry for an action this handler just performed.
//
// Failures are logged, never propagated: the action has already committed, and
// answering 500 because the trail write failed would invite a retry that
// performs it a second time.
//
// The actor's e-mail costs one extra read per administrative action. That is
// affordable — these are rare, human-driven requests — and it is what lets the
// trail stay readable after the actor's own account is deleted, which a join at
// render time could not do.
func (h *AdminHandler) audit(r *http.Request, action string, target *User, detail string) {
	rec := AuditRecord{Action: action, Detail: detail}
	if caller, ok := authctx.FromContext(r.Context()); ok && caller.UserID != 0 {
		id := caller.UserID
		rec.ActorID = &id
		if u, err := h.repo.GetUser(r.Context(), id); err == nil {
			rec.ActorEmail = u.Email
		}
	}
	if target != nil {
		rec.TargetEmail = target.Email
		// The id is attached ONLY when it still references a live row. Two call
		// sites legitimately have an e-mail and no account: a deletion, whose
		// row is already gone by the time this runs, and an invitation, whose
		// addressee has no account yet. audit_log.target_id is a foreign key, so
		// attaching an id in either case violates it — and because an audit
		// failure is logged rather than propagated, the entry would simply
		// vanish, which is the one outcome a trail must not have.
		if target.ID != 0 {
			id := target.ID
			rec.TargetID = &id
		}
	}
	if err := h.repo.Audit(r.Context(), rec); err != nil {
		h.logger.Error("audit write", "err", err, "action", action)
	}
}

// AuditPolicyChange records an instance-policy edit.
//
// Exported so internal/policy can record into the trail without importing this
// package — auth already imports policy to enforce its rules, and the reverse
// import would close the cycle.
func (h *AdminHandler) AuditPolicyChange(r *http.Request, detail string) {
	h.audit(r, AuditPolicyChanged, nil, detail)
}

// ListAudit serves the administrative trail.
func (h *AdminHandler) ListAudit(w http.ResponseWriter, r *http.Request) {
	before, err := optionalInt64(r.URL.Query().Get("before"))
	if err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_cursor", "before must be an id"))
		return
	}
	limit, err := optionalInt64(r.URL.Query().Get("limit"))
	// Range-checked BEFORE the conversion, not after. ListAudit clamps its own
	// argument, but by then the int64 has already been narrowed to int — and on
	// a 32-bit build a value like 2^32+50 truncates to 50, arriving as a
	// perfectly plausible number that no clamp can recognise as garbage.
	if err != nil || limit < 0 || limit > maxAuditPageSize {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_limit",
			fmt.Sprintf("limit must be a number between 0 and %d", maxAuditPageSize)))
		return
	}
	entries, err := h.repo.ListAudit(r.Context(), r.URL.Query().Get("action"), before, int(limit))
	if err != nil {
		h.logger.Error("admin list audit", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// optionalInt64 parses a query parameter that may be absent, treating "" as 0
// so the caller can express "no cursor" and "no explicit limit" the same way.
func optionalInt64(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

// Metrics serves the administration header.
func (h *AdminHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	m, err := h.repo.Metrics(r.Context())
	if err != nil {
		h.logger.Error("admin metrics", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, m)
}

// Roles serves the RBAC matrix plus how many accounts hold each role.
func (h *AdminHandler) Roles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.repo.Roles(r.Context())
	if err != nil {
		h.logger.Error("admin roles", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{
		"roles": roles,
		// The full ordered vocabulary, so the client renders the matrix columns
		// without hardcoding a list that could drift from the server's.
		"permissions": authctx.AllPermissions,
	})
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

	// Trimmed HERE so the address that receives the invitation is byte-identical
	// to the one stored on the row: CreateInvite persists strings.TrimSpace(email),
	// and building the message from the raw field would mail a pasted address with
	// a trailing space to a recipient the invite table does not name.
	email := strings.TrimSpace(in.Email)
	// Loaded BEFORE the invitation, because the message commits inside the same
	// transaction and the inviter's name is part of it.
	inviter, _ := h.repo.GetUser(r.Context(), caller.UserID)
	draft := MailDraft{Locale: localeFrom(r), Build: func(token string) mailer.Envelope {
		return mailer.InviteMessage(email, inviter.Name,
			h.baseURL+"/#invite="+token, int(inviteTTL/time.Hour))
	}}
	inv, raw, err := h.repo.CreateInvite(r.Context(), email, role, caller.UserID, inviteTTL, draft)
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

	// The invited ADDRESS and role, never inv.AcceptURL — that string carries
	// the raw invitation token, and the trail is a screen administrators read.
	h.audit(r, AuditInviteCreated, &User{Email: inv.Email}, string(role))
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
	h.audit(r, AuditInviteRevoked, nil, fmt.Sprintf("invite %d", id))
	w.WriteHeader(http.StatusNoContent)
}

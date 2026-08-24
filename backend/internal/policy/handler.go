package policy

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/pkg/httperr"
	"foldex/internal/roleperm"
)

// Handler serves /api/admin/policy. It is mounted inside the administration
// group, so the role gate and the API-token rejection have already run.
type Handler struct {
	repo   *Repository
	logger *slog.Logger
	// onChange records the edit in the audit trail. A function rather than a
	// repository so this package does not import internal/auth, which imports
	// this one.
	onChange func(r *http.Request, detail string)
	// grants is the effective RBAC matrix (ADR-42). Nil at construction means
	// the compiled one — what every test that does not care about configured
	// permissions wants; main always passes the loaded repository.
	grants authgate.Grants
}

func NewHandler(repo *Repository, logger *slog.Logger, onChange func(*http.Request, string), grants authgate.Grants) *Handler {
	grants = roleperm.OrDefault(grants)
	return &Handler{repo: repo, logger: logger, onChange: onChange, grants: grants}
}

func (h *Handler) Mount(r chi.Router) {
	// Read for any administrator, write for the owner alone: an admin has to be
	// able to see the rules they manage people under, but an admin who could
	// lower the password floor or widen the Google allowlist could lower the
	// instance's security and then walk in through the gap.
	r.With(authgate.RequirePermission(h.grants, authctx.PermPolicyRead)).Get("/", h.Get)
	r.With(authgate.RequirePermission(h.grants, authctx.PermPolicyWrite)).Put("/", h.Put)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.Get(r.Context())
	if err != nil {
		h.logger.Error("policy get", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, p)
}

func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[Policy](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// Absent fields take the floor before validation, so a client that predates
	// a setting can still save the ones it does know about.
	in = in.WithDefaults()
	// ValidateForWrite, not Validate: the tighter bounds are the ones that
	// apply to a document an owner is SAVING, and they exist only here. Reached
	// through repo.Set instead, the refusal would surface as the default arm of
	// this handler — a logged 500 and a bare `server_error` — so an owner who
	// typed a password floor above bcrypt's 72-byte truncation point would be
	// told nothing at all. INV-169 promises the opposite.
	if err := in.ValidateForWrite(); err != nil {
		// The validation message names the offending field and its bounds. That
		// is safe to return: these are documented limits, not secrets, and an
		// owner who is told the real ceiling can fix the form, while a bare
		// "invalid" sends them guessing.
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_policy", err.Error()))
		return
	}
	if err := h.repo.Set(r.Context(), in); err != nil {
		h.logger.Error("policy set", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if h.onChange != nil {
		h.onChange(r, "instance policy updated")
	}
	httperr.JSON(w, http.StatusOK, in)
}

package policy

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/pkg/httperr"
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
}

func NewHandler(repo *Repository, logger *slog.Logger, onChange func(*http.Request, string)) *Handler {
	return &Handler{repo: repo, logger: logger, onChange: onChange}
}

func (h *Handler) Mount(r chi.Router) {
	// Read for any administrator, write for the owner alone: an admin has to be
	// able to see the rules they manage people under, but an admin who could
	// lower the password floor or widen the Google allowlist could lower the
	// instance's security and then walk in through the gap.
	r.With(authgate.RequirePermission(authctx.PermPolicyRead)).Get("/", h.Get)
	r.With(authgate.RequirePermission(authctx.PermPolicyWrite)).Put("/", h.Put)
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
	if err := in.Validate(); err != nil {
		// The validation message names the offending field and its bounds. That
		// is safe to return: these are documented limits, not secrets, and an
		// owner who is told "between 8 and 128" can fix the form, while a bare
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

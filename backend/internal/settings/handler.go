package settings

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"
)

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler { return &Handler{repo: repo} }

func (h *Handler) Mount(r chi.Router) {
	r.Get("/master-password", h.status)
	r.Put("/master-password", h.setMaster)
	r.Delete("/master-password", h.clearMaster)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	configured, err := h.repo.MasterPasswordConfigured(r.Context())
	if err != nil {
		httperr.Write(w, err)
		return
	}
	var hint *string
	if configured {
		if hint, err = h.repo.MasterPasswordHint(r.Context()); err != nil {
			httperr.Write(w, err)
			return
		}
	}
	httperr.JSON(w, http.StatusOK, statusOutput{Configured: configured, Hint: hint})
}

func (h *Handler) setMaster(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[setMasterInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := in.Validate(); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_input", err.Error()))
		return
	}
	configured, err := h.repo.MasterPasswordConfigured(r.Context())
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// Changing an existing master requires proving the current one; the
	// first-ever set needs no proof (nothing to authorize against yet).
	if configured {
		if in.CurrentPassword == nil {
			httperr.Write(w, httperr.New(http.StatusUnauthorized, "wrong_password", "current master password is required to change it"))
			return
		}
		ok, _, err := h.repo.VerifyMaster(r.Context(), *in.CurrentPassword)
		if err != nil {
			httperr.Write(w, err)
			return
		}
		if !ok {
			httperr.Write(w, httperr.New(http.StatusUnauthorized, "wrong_password", "current master password is incorrect"))
			return
		}
	}
	// Tri-state: hint field absent (in.Hint == nil) → keep the existing hint;
	// present → set the trimmed value (empty string clears it). Distinguishing
	// absent from empty is why we don't collapse to NormalizedHint here.
	var hintArg *string
	if in.Hint != nil {
		s := strings.TrimSpace(*in.Hint)
		hintArg = &s
	}
	if err := h.repo.SetMasterPassword(r.Context(), in.Password, hintArg); err != nil {
		httperr.Write(w, err)
		return
	}
	resHint, err := h.repo.MasterPasswordHint(r.Context())
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, statusOutput{Configured: true, Hint: resHint})
}

func (h *Handler) clearMaster(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[clearMasterInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	ok, configured, err := h.repo.VerifyMaster(r.Context(), in.CurrentPassword)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// Idempotent: nothing configured means nothing to remove.
	if !configured {
		httperr.JSON(w, http.StatusOK, statusOutput{Configured: false})
		return
	}
	if !ok {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "wrong_password", "current master password is incorrect"))
		return
	}
	if err := h.repo.ClearMasterPassword(r.Context()); err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, statusOutput{Configured: false})
}

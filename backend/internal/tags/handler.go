package tags

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/auditctx"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler { return &Handler{repo: repo} }

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	out, err := h.repo.List(r.Context(), authctx.MustUser(r.Context()))
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[CreateInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in.Normalize()
	if err := in.Validate(); err != nil {
		var v validationErr
		if errors.As(err, &v) {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_input", string(v)))
			return
		}
		httperr.Write(w, err)
		return
	}
	t, err := h.repo.Create(r.Context(), authctx.MustUser(r.Context()), in)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	// Names the row for the owner's own-activity feed (ADR-46). The
	// content-audit middleware records the event either way; this is what
	// gives it a label its owner can recognise a month later.
	auditctx.SetRequest(r, "tag", t.ID, t.Name)
	httperr.JSON(w, http.StatusCreated, t)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	t, err := h.repo.Get(r.Context(), authctx.MustUser(r.Context()), id)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	httperr.JSON(w, http.StatusOK, t)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in, err := httperr.DecodeJSON[UpdateInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in.Normalize()
	if err := in.Validate(); err != nil {
		var v validationErr
		if errors.As(err, &v) {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_input", string(v)))
			return
		}
		httperr.Write(w, err)
		return
	}
	t, err := h.repo.Update(r.Context(), authctx.MustUser(r.Context()), id, in)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	// Names the row for the owner's own-activity feed (ADR-46). The
	// content-audit middleware records the event either way; this is what
	// gives it a label its owner can recognise a month later.
	auditctx.SetRequest(r, "tag", t.ID, t.Name)
	httperr.JSON(w, http.StatusOK, t)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// Read BEFORE the delete: a moment later the label exists nowhere, and an
	// entry its owner cannot resolve is not a record of anything. One
	// primary-key read on an operation nobody performs in a loop.
	name := ""
	if existing, err := h.repo.Get(r.Context(), authctx.MustUser(r.Context()), id); err == nil {
		name = existing.Name
	}
	if err := h.repo.Delete(r.Context(), authctx.MustUser(r.Context()), id); err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	auditctx.SetRequest(r, "tag", id, name)
	w.WriteHeader(http.StatusNoContent)
}

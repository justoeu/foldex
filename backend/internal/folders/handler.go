package folders

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

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
	q := ListQuery{}
	if v := r.URL.Query().Get("parent_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			q.ParentID = &n
		}
	}
	if v := r.URL.Query().Get("root"); v == "1" || v == "true" {
		q.RootOnly = true
	}
	out, err := h.repo.List(r.Context(), q)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	r.Body = http.MaxBytesReader(w, r.Body, httperr.JSONBodyCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_json", err.Error()))
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
	f, err := h.repo.Create(r.Context(), in)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusCreated, f)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	f, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, f)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	var in UpdateInput
	r.Body = http.MaxBytesReader(w, r.Body, httperr.JSONBodyCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_json", err.Error()))
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
	f, err := h.repo.Update(r.Context(), id, in)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, f)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// ?cascade=1|true → delete contained links too. Default keeps links (the
	// FK is ON DELETE SET NULL so they unflag back to ungrouped on home).
	cascade := false
	if v := r.URL.Query().Get("cascade"); v == "1" || v == "true" {
		cascade = true
	}
	if cascade {
		if err := h.repo.DeleteCascade(r.Context(), id); err != nil {
			httperr.Write(w, err)
			return
		}
	} else {
		if err := h.repo.Delete(r.Context(), id); err != nil {
			httperr.Write(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httperr.New(http.StatusBadRequest, "invalid_id", "id must be a positive integer")
	}
	return id, nil
}

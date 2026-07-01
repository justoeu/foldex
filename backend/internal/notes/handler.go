package notes

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/links"
	"foldex/internal/pkg/clampint"
	"foldex/internal/pkg/httperr"
)

// notesJSONBodyCap is larger than httperr.JSONBodyCap (64 KiB) — note bodies
// routinely exceed that even before sanitization overhead. 1 MiB bounds the
// sanitizer's work and comfortably exceeds MaxBodyHTMLBytes (the persisted
// cap, checked post-sanitize in dto.go).
const notesJSONBodyCap = 1 << 20

type Handler struct {
	repo    *Repository
	storage links.Uploader // optional — nil disables Delete's image cleanup
}

func NewHandler(repo *Repository, storage links.Uploader) *Handler {
	return &Handler{repo: repo, storage: storage}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := ListQuery{
		Q:    strings.TrimSpace(r.URL.Query().Get("q")),
		Sort: r.URL.Query().Get("sort"),
	}
	for _, raw := range r.URL.Query()["tag"] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			q.TagIDs = append(q.TagIDs, id)
		}
	}
	q.Limit = clampint.Int(r.URL.Query().Get("limit"), 100, 1, 500)
	q.Offset = clampint.Int(r.URL.Query().Get("offset"), 0, 0, 100000)
	if v := r.URL.Query().Get("folder_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			q.FolderID = &n
		}
	}
	if v := r.URL.Query().Get("ungrouped"); v == "1" || v == "true" {
		q.Ungrouped = true
	}
	out, err := h.repo.List(r.Context(), q)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSONWithCap[CreateInput](w, r, notesJSONBodyCap)
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
	n, err := h.repo.Create(r.Context(), in)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusCreated, n)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	n, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, n)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in, err := httperr.DecodeJSONWithCap[UpdateInput](w, r, notesJSONBodyCap)
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
	n, err := h.repo.Update(r.Context(), id, in)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, n)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := h.repo.Delete(r.Context(), id, h.storage); err != nil {
		httperr.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package entries

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/clampint"
	"foldex/internal/pkg/httperr"
)

// Handler exposes the single read-only GET /api/entries route. No
// Create/Update/Delete — mutations stay on /api/links and /api/notes.
type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler { return &Handler{repo: repo} }

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
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

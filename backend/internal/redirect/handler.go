package redirect

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"foldex/internal/links"
	"foldex/internal/pkg/httperr"
)

type Handler struct {
	repo *links.Repository
}

func NewHandler(repo *links.Repository) *Handler { return &Handler{repo: repo} }

func (h *Handler) Mount(r chi.Router) {
	r.Get("/go/{id}", h.redirect)
}

func (h *Handler) redirect(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_id", "id must be a positive integer"))
		return
	}
	dest, err := h.repo.ClickAndResolve(r.Context(), id)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

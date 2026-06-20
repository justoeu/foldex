package redirect

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/links"
	"foldex/internal/pkg/httperr"
)

type Handler struct {
	repo *links.Repository
}

func NewHandler(repo *links.Repository) *Handler { return &Handler{repo: repo} }

func (h *Handler) Mount(r chi.Router) {
	// Param name stays "id" so the chi route doesn't change shape — what
	// flows through it can be either a numeric ID or a slug, decided in
	// h.redirect at request time.
	r.Get("/go/{id}", h.redirect)
}

// redirect resolves /go/{value} where {value} is either a positive int
// (legacy ID lookup) or a slug. ID-first to preserve backward compatibility
// for every old `/go/42` link that's already shared somewhere — slugs are a
// pure fallback.
//
// The DB CHECK constraint on link.slug forbids pure-numeric values, so the
// branches can't collide: a value that parses as int can't ever be a slug.
func (h *Handler) redirect(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "id")
	if raw == "" {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_target", "target is required"))
		return
	}

	if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
		dest, err := h.repo.ClickAndResolve(r.Context(), id)
		if err == nil {
			redirect(w, r, dest)
			return
		}
		// A pure-numeric value can never be a slug (CHECK constraint), so
		// not-found here is terminal.
		httperr.Write(w, err)
		return
	}

	dest, err := h.repo.ClickAndResolveBySlug(r.Context(), raw)
	if err != nil {
		if errors.Is(err, httperr.ErrNotFound) {
			httperr.Write(w, httperr.ErrNotFound)
			return
		}
		httperr.Write(w, err)
		return
	}
	redirect(w, r, dest)
}

// redirect validates the destination scheme before issuing the redirect.
// All write paths enforce http(s)://, but defense-in-depth here catches any
// future regression or direct-DB manipulation that could plant a
// javascript:/data:/file: URL in the link table.
func redirect(w http.ResponseWriter, r *http.Request, dest string) {
	if !strings.HasPrefix(dest, "http://") && !strings.HasPrefix(dest, "https://") {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_target", "unsupported scheme"))
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

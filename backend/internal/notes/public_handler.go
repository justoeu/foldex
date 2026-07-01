package notes

import (
	"errors"
	"html/template"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"
)

// PublicHandler serves the read-only rendered note page at GET /n/{id-or-slug},
// mounted outside /api (same place /go/{id-or-slug} lives) so it's reachable
// without the SHARED_SECRET guard — a note is meant to be shareable the same
// way a link is. Unlike /go/, this renders content rather than redirecting:
// a note has no external URL to forward to.
type PublicHandler struct {
	repo *Repository
}

func NewPublicHandler(repo *Repository) *PublicHandler { return &PublicHandler{repo: repo} }

func (h *PublicHandler) Mount(r chi.Router) {
	r.Get("/n/{id}", h.view)
}

func (h *PublicHandler) view(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "id")
	if raw == "" {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_target", "target is required"))
		return
	}
	n, err := h.repo.ViewAndResolve(r.Context(), raw)
	if err != nil {
		if errors.Is(err, httperr.ErrNotFound) {
			httperr.Write(w, httperr.ErrNotFound)
			return
		}
		httperr.Write(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// n.BodyHTML was sanitized server-side at write time (Normalize calls
	// htmlsanitize.Sanitize before the repository ever persists it) — safe to
	// inject as template.HTML here without re-sanitizing on read. n.Title
	// goes through {{.Title}} (auto-escaped) since titles are plain text,
	// never HTML.
	if err := pageTemplate.Execute(w, pageData{Title: n.Title, Body: template.HTML(n.BodyHTML)}); err != nil { //nolint:gosec // BodyHTML sanitized at write time
		httperr.Write(w, httperr.ErrInternal)
		return
	}
}

type pageData struct {
	Title string
	Body  template.HTML
}

var pageTemplate = template.Must(template.New("note").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  body { max-width: 720px; margin: 40px auto; padding: 0 20px; font: 16px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color: #1f2330; }
  h1, h2, h3, h4, h5, h6 { line-height: 1.25; }
  img { max-width: 100%; height: auto; border-radius: 8px; }
  pre, code { background: #f4f4f6; border-radius: 4px; }
  pre { padding: 12px; overflow-x: auto; }
  code { padding: 2px 5px; }
  blockquote { border-left: 3px solid #ccc; margin-left: 0; padding-left: 16px; color: #555; }
  a { color: #6366F1; }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
{{.Body}}
</body>
</html>
`))

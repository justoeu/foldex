package entries

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/folders"
	"foldex/internal/pkg/clampint"
	"foldex/internal/pkg/httperr"
)

// FolderPasswordLookup resolves a folder's current password hash for the
// content-gate on GET /api/entries?folder_id=X — satisfied by
// *folders.Repository. Kept as a narrow interface so this package doesn't
// need the full folders.Repository surface (just the one lookup it needs).
type FolderPasswordLookup interface {
	PasswordHashFor(ctx context.Context, id int64) (*string, error)
}

// Handler exposes the single read-only GET /api/entries route. No
// Create/Update/Delete — mutations stay on /api/links and /api/notes.
type Handler struct {
	repo         *Repository
	folderLookup FolderPasswordLookup
	unlockKey    []byte
}

func NewHandler(repo *Repository, folderLookup FolderPasswordLookup, unlockKey []byte) *Handler {
	return &Handler{repo: repo, folderLookup: folderLookup, unlockKey: unlockKey}
}

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
	// Content-gate: this is the ONE read path that returns a folder's real
	// links+notes (see internal/entries package doc). Same proof-of-password
	// requirement as folders.List(parent_id=X) — see CLAUDE.md's folder-
	// password invariant.
	if q.FolderID != nil {
		hash, err := h.folderLookup.PasswordHashFor(r.Context(), *q.FolderID)
		if err != nil {
			httperr.Write(w, err)
			return
		}
		if err := folders.CheckUnlock(h.unlockKey, *q.FolderID, hash, r.Header.Get(folders.UnlockHeader)); err != nil {
			httperr.Write(w, err)
			return
		}
	}
	out, err := h.repo.List(r.Context(), q)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

package entries

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/folders"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/listquery"
)

// FolderPasswordLookup resolves a folder's current password hash for the
// content-gate on GET /api/entries?folder_id=X — satisfied by
// *folders.Repository. Kept as a narrow interface so this package doesn't
// need the full folders.Repository surface (just the one lookup it needs).
type FolderPasswordLookup interface {
	PasswordHashFor(ctx context.Context, id int64) (*string, error)
}

// Lister is satisfied by *Repository.
type Lister interface {
	List(ctx context.Context, q ListQuery) ([]Entry, error)
}

// Handler exposes the single read-only GET /api/entries route. No
// Create/Update/Delete — mutations stay on /api/links and /api/notes.
type Handler struct {
	repo         Lister
	folderLookup FolderPasswordLookup
	unlockKey    []byte
}

func NewHandler(repo Lister, folderLookup FolderPasswordLookup, unlockKey []byte) *Handler {
	return &Handler{repo: repo, folderLookup: folderLookup, unlockKey: unlockKey}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p := listquery.Parse(r)
	q := ListQuery{
		Q: p.Q, TagIDs: p.TagIDs, Sort: p.Sort, Limit: p.Limit, Offset: p.Offset,
		FolderID: p.FolderID, Ungrouped: p.Ungrouped,
	}
	// Content-gate: this is the ONE read path that returns a folder's real
	// links+notes (see internal/entries package doc). Same proof-of-password
	// requirement as folders.List(parent_id=X) — see CLAUDE.md's folder-
	// password invariant. Re-check after List so a password change mid-request
	// cannot leak one more response (RACE-HER-005).
	if q.FolderID != nil {
		token := r.Header.Get(folders.UnlockHeader)
		if err := h.enforceFolderUnlock(r.Context(), *q.FolderID, token); err != nil {
			httperr.Write(w, err)
			return
		}
		out, err := h.repo.List(r.Context(), q)
		if err != nil {
			httperr.Write(w, err)
			return
		}
		if err := h.enforceFolderUnlock(r.Context(), *q.FolderID, token); err != nil {
			httperr.Write(w, err)
			return
		}
		httperr.JSON(w, http.StatusOK, out)
		return
	}
	out, err := h.repo.List(r.Context(), q)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

func (h *Handler) enforceFolderUnlock(ctx context.Context, folderID int64, token string) error {
	hash, err := h.folderLookup.PasswordHashFor(ctx, folderID)
	if err != nil {
		return err
	}
	return folders.CheckUnlock(h.unlockKey, folderID, hash, token)
}

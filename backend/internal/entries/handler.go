package entries

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/folders"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/listquery"
)

// Lister is satisfied by *Repository.
type Lister interface {
	List(ctx context.Context, uid authctx.UserID, q ListQuery) ([]Entry, error)
}

// Handler exposes the single read-only GET /api/entries route. No
// Create/Update/Delete — mutations stay on /api/links and /api/notes.
type Handler struct {
	repo       Lister
	folderGate folders.ContentGate
}

func NewHandler(repo Lister, folderLookup folders.PasswordHashLookup, unlockKey []byte) *Handler {
	return &Handler{repo: repo, folderGate: folders.NewContentGate(folderLookup, unlockKey)}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := listquery.Parse(r)
	ctx := r.Context()
	uid := authctx.MustUser(ctx)
	token := r.Header.Get(folders.UnlockHeader)
	out, err := folders.ListWithContentGate(ctx, h.folderGate, uid, q.FolderID, token, func() ([]Entry, error) {
		return h.repo.List(ctx, uid, q)
	})
	if err != nil {
		httperr.Write(w, folders.HTTPError(err))
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

package entries

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"foldex/internal/folders"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/listquery"
)

// Lister is satisfied by *Repository.
type Lister interface {
	Counts(ctx context.Context, uid authctx.UserID) (EntryCounts, error)
	List(ctx context.Context, uid authctx.UserID, q ListQuery) ([]Entry, error)
	PreviewStatuses(ctx context.Context, uid authctx.UserID, ids []int64, folderID *int64) ([]PreviewStatus, error)
}

const PreviewStatusMaxIDs = 100

// Handler exposes read-only entry routes. Mutations stay on /api/links and
// /api/notes.
type Handler struct {
	repo       Lister
	folderGate folders.ContentGate
}

func NewHandler(repo Lister, folderLookup folders.PasswordHashLookup, unlockKey []byte) *Handler {
	return &Handler{repo: repo, folderGate: folders.NewContentGate(folderLookup, unlockKey)}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
	r.Get("/counts", h.counts)
	r.Get("/preview-status", h.previewStatuses)
}

func (h *Handler) counts(w http.ResponseWriter, r *http.Request) {
	out, err := h.repo.Counts(r.Context(), authctx.MustUser(r.Context()))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
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

func (h *Handler) previewStatuses(w http.ResponseWriter, r *http.Request) {
	rawIDs := r.URL.Query()["id"]
	if len(rawIDs) == 0 || len(rawIDs) > PreviewStatusMaxIDs {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_ids", "provide between 1 and 100 ids"))
		return
	}
	ids := make([]int64, len(rawIDs))
	for i, raw := range rawIDs {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_id", "ids must be positive integers"))
			return
		}
		ids[i] = id
	}

	var folderID *int64
	if raw := r.URL.Query().Get("folder_id"); raw != "" {
		id, err := httperr.ParseID(raw)
		if err != nil {
			httperr.Write(w, err)
			return
		}
		folderID = &id
	}
	ctx := r.Context()
	uid := authctx.MustUser(ctx)
	token := r.Header.Get(folders.UnlockHeader)
	out, err := folders.ListWithContentGate(ctx, h.folderGate, uid, folderID, token, func() ([]PreviewStatus, error) {
		return h.repo.PreviewStatuses(ctx, uid, ids, folderID)
	})
	if err != nil {
		httperr.Write(w, folders.HTTPError(err))
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

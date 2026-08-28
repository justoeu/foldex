package notes

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/folders"
	"foldex/internal/pkg/auditctx"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/listquery"
	"foldex/internal/ports"
)

// notesJSONBodyCap is larger than httperr.JSONBodyCap (64 KiB) — note bodies
// routinely exceed that even before sanitization overhead. 1 MiB bounds the
// sanitizer's work and comfortably exceeds MaxBodyHTMLBytes (the persisted
// cap, checked post-sanitize in dto.go).
const notesJSONBodyCap = 1 << 20

type Handler struct {
	repo       *Repository
	storage    ports.Uploader // optional — nil disables Delete's image cleanup
	folderGate folders.ContentGate
}

func NewHandler(repo *Repository, storage ports.Uploader) *Handler {
	return &Handler{repo: repo, storage: storage}
}

// WithFolderGate enables unlock-token content-gate on ?folder_id= lists.
func (h *Handler) WithFolderGate(lookup folders.PasswordHashLookup, unlockKey []byte) *Handler {
	h.folderGate = folders.NewContentGate(lookup, unlockKey)
	return h
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := listquery.Parse(r)
	ctx := r.Context()
	uid := authctx.MustUser(ctx)
	token := r.Header.Get(folders.UnlockHeader)
	out, err := folders.ListWithContentGate(ctx, h.folderGate, uid, q.FolderID, token, func() ([]Note, error) {
		return h.repo.List(ctx, uid, q)
	})
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
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
	n, err := h.repo.Create(r.Context(), authctx.MustUser(r.Context()), in)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	// Names the row for the owner's own-activity feed (ADR-46). The
	// content-audit middleware records the event either way; this is what
	// gives it a label its owner can recognise a month later.
	auditctx.SetRequest(r, "note", n.ID, n.Title)
	httperr.JSON(w, http.StatusCreated, n)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	n, err := h.repo.Get(r.Context(), authctx.MustUser(r.Context()), id)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
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
	n, err := h.repo.Update(r.Context(), authctx.MustUser(r.Context()), id, in)
	if err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	// Names the row for the owner's own-activity feed (ADR-46). The
	// content-audit middleware records the event either way; this is what
	// gives it a label its owner can recognise a month later.
	auditctx.SetRequest(r, "note", n.ID, n.Title)
	httperr.JSON(w, http.StatusOK, n)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// Read BEFORE the delete: a moment later the label exists nowhere, and an
	// entry its owner cannot resolve is not a record of anything. One
	// primary-key read on an operation nobody performs in a loop.
	title := ""
	if existing, err := h.repo.Get(r.Context(), authctx.MustUser(r.Context()), id); err == nil {
		title = existing.Title
	}
	if err := h.repo.Delete(r.Context(), authctx.MustUser(r.Context()), id, h.storage); err != nil {
		httperr.Write(w, repositoryHTTPError(err))
		return
	}
	auditctx.SetRequest(r, "note", id, title)
	w.WriteHeader(http.StatusNoContent)
}

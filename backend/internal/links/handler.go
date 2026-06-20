package links

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/clampint"
	"foldex/internal/pkg/httperr"
)

// Enqueuer is implemented by the preview worker. We keep it tiny to avoid an
// import cycle between links and preview. Enqueue returns an error so handlers
// can decide whether to surface backpressure to the client; the typical choice
// is to log + carry on (the link row already exists, the user gets 201).
type Enqueuer interface {
	Enqueue(linkID int64) error
}

type Handler struct {
	repo    *Repository
	worker  Enqueuer
	fetcher MetadataFetcher // optional — disables /url-metadata when nil
}

func NewHandler(repo *Repository, worker Enqueuer) *Handler {
	return &Handler{repo: repo, worker: worker}
}

// WithMetadataFetcher wires the synchronous URL-metadata fetch endpoint. Kept
// as an optional dependency so router.go can pass it in at startup without
// breaking the existing NewHandler call sites and so tests can omit it when
// they don't exercise the route.
func (h *Handler) WithMetadataFetcher(f MetadataFetcher) *Handler {
	h.fetcher = f
	return h
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	// /recent-changes and /url-metadata are static — must be registered
	// before /{id} so Chi routes them to the right handler. Chi matches
	// longest/most-specific path regardless of registration order, but
	// keeping the source order intuitive avoids surprises during refactors.
	r.Get("/recent-changes", h.listRecentChanges)
	r.Get("/url-metadata", h.fetchURLMetadata)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
	r.Post("/{id}/refresh-preview", h.refreshPreview)
	r.Post("/{id}/seen-change", h.seenChange)
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
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Offset = n
		}
	}
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
	in, err := httperr.DecodeJSON[CreateInput](w, r)
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
	l, err := h.repo.Create(r.Context(), in)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if h.worker != nil {
		// Fire-and-forget: ErrQueueFull/ErrStopped don't fail the request — the
		// link row exists, the next requeuePending tick picks it up.
		_ = h.worker.Enqueue(l.ID)
	}
	httperr.JSON(w, http.StatusCreated, l)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	l, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, l)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in, err := httperr.DecodeJSON[UpdateInput](w, r)
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
	l, err := h.repo.Update(r.Context(), id, in)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, l)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		httperr.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) refreshPreview(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if _, err := h.repo.Get(r.Context(), id); err != nil {
		httperr.Write(w, err)
		return
	}
	// Reset status to 'pending' before enqueueing so the frontend immediately
	// sees "capturando…" and the auto-polling in useLinks kicks in. Without
	// this, a previously 'failed' link stays 'failed' visually and the user
	// has no signal the retry is running.
	if err := h.repo.UpdatePreview(r.Context(), id, StatusPending, nil, nil, nil, nil); err != nil {
		httperr.Write(w, err)
		return
	}
	if h.worker != nil {
		_ = h.worker.Enqueue(id)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) seenChange(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := h.repo.MarkChangeSeen(r.Context(), id); err != nil {
		httperr.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listRecentChanges(w http.ResponseWriter, r *http.Request) {
	days := clampint.Int(r.URL.Query().Get("days"), 7, 1, 30)
	limit := clampint.Int(r.URL.Query().Get("limit"), 20, 1, 100)
	out, err := h.repo.ListRecentChanges(r.Context(), days*24*60*60, limit)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}



package links

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

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
	repo   *Repository
	worker Enqueuer
}

func NewHandler(repo *Repository, worker Enqueuer) *Handler {
	return &Handler{repo: repo, worker: worker}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
	r.Post("/{id}/refresh-preview", h.refreshPreview)
}

// jsonBodyCap is the hard ceiling for JSON request bodies. ParseMultipartForm
// already has its own cap on /image and /backup; this protects the plain JSON
// endpoints (links/folders/tags Create+Update) from a 100 MB payload tying up
// memory inside json.Decoder. 64 KiB is generous — a Link with description,
// tags array, and slug is well under 4 KiB.
const jsonBodyCap = 64 << 10

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
	var in CreateInput
	r.Body = http.MaxBytesReader(w, r.Body, jsonBodyCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_json", err.Error()))
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
	id, err := parseID(r)
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
	id, err := parseID(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	var in UpdateInput
	r.Body = http.MaxBytesReader(w, r.Body, jsonBodyCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_json", err.Error()))
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
	id, err := parseID(r)
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
	id, err := parseID(r)
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

func parseID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httperr.New(http.StatusBadRequest, "invalid_id", "id must be a positive integer")
	}
	return id, nil
}

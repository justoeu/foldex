package stats

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"
)

// StorageStatter abstracts the object-storage size lookup so the stats handler
// stays decoupled from the concrete MinIO client.
type StorageStatter interface {
	Stats(ctx context.Context) (StorageStats, error)
}

// StorageStats mirrors the shape we expose on the wire. Keep it in this
// package so frontend/handler share one type.
type StorageStats struct {
	Objects    int64 `json:"objects"`
	TotalBytes int64 `json:"total_bytes"`
}

type Handler struct {
	repo    *Repository
	storage StorageStatter // optional — nil disables /stats/storage
}

func NewHandler(repo *Repository) *Handler { return &Handler{repo: repo} }

// WithStorage attaches an object-storage source so the storage endpoint is
// registered when the bucket is reachable.
func (h *Handler) WithStorage(s StorageStatter) *Handler {
	h.storage = s
	return h
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/summary", h.summary)
	r.Get("/daily", h.daily)
	r.Get("/top", h.top)
	r.Get("/tags", h.tags)
	if h.storage != nil {
		r.Get("/storage", h.storageStats)
	}
}

func (h *Handler) storageStats(w http.ResponseWriter, r *http.Request) {
	s, err := h.storage.Stats(r.Context())
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, s)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	s, err := h.repo.Summary(r.Context())
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, s)
}

func (h *Handler) daily(w http.ResponseWriter, r *http.Request) {
	days := 60
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	out, err := h.repo.Daily(r.Context(), days)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

func (h *Handler) top(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	out, err := h.repo.TopLinks(r.Context(), limit)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

func (h *Handler) tags(w http.ResponseWriter, r *http.Request) {
	out, err := h.repo.TagBuckets(r.Context())
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

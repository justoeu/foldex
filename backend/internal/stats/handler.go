package stats

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/clampint"
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
	// Clamp to [1, 365]. Without the cap, `?days=2147483647` lands in a
	// `generate_series(now() - 2.1e9 * interval '1 day', ...)` and the
	// planner happily attempts it — auth-gated DoS otherwise.
	days := clampint.Int(r.URL.Query().Get("days"), 60, 1, 365)
	out, err := h.repo.Daily(r.Context(), days)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

func (h *Handler) top(w http.ResponseWriter, r *http.Request) {
	// Clamp to [1, 100] — `?limit=999999999` would `ORDER BY clicks DESC` on
	// every link before slicing.
	limit := clampint.Int(r.URL.Query().Get("limit"), 10, 1, 100)
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

package backupstatus

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/pkg/clampint"
	"foldex/internal/pkg/httperr"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// Handler serves /api/admin/backup. It mounts inside the /api/admin group, so
// a non-admin already got its 404 (INV-043) before these gates run; the
// permission gate answers the finer question of whether THIS administrator's
// role holds instance.backup.
type Handler struct {
	repo   *Repository
	logger *slog.Logger
	// audit records the manual trigger in the administrative trail. A function
	// rather than the auth handler, for AuditPolicyChange's reason: auth is a
	// consumer of this package's mount point, and the reverse import would
	// close the cycle. Nil (router tests) skips the trail.
	audit  func(*http.Request, string)
	grants authgate.Grants
}

func NewHandler(repo *Repository, logger *slog.Logger,
	audit func(*http.Request, string), grants authgate.Grants) *Handler {
	return &Handler{repo: repo, logger: logger, audit: audit, grants: grants}
}

func (h *Handler) Mount(r chi.Router) {
	read := authgate.RequirePermission(h.grants, authctx.PermInstanceBackupRead)
	r.With(read).Get("/runs", h.ListRuns)
	// The trigger sits behind the same READ permission on purpose: the button
	// only enqueues a 'requested' row the agent may claim — the credentials,
	// schedule and execution stay the agent's. A separate write permission
	// would promise a distinction the server cannot deliver.
	r.With(read).Post("/run", h.RequestRun)
}

// ListRuns answers the whole band in one request: the per-job summary and one
// page of history.
func (h *Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	job := r.URL.Query().Get("job")
	if job != "" && !ValidJob(job) {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_job",
			"job must be one of dump, drill, mirror, user_zip"))
		return
	}
	limit := clampint.Int(r.URL.Query().Get("limit"), defaultPageSize, 1, maxPageSize)
	before := int64(0)
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_cursor", "before must be an id"))
			return
		}
		before = parsed
	}

	summary, err := h.repo.Summary(r.Context())
	if err != nil {
		h.logger.Error("backup status summary", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	runs, err := h.repo.ListRuns(r.Context(), job, limit, before)
	if err != nil {
		h.logger.Error("backup status list", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"jobs": summary, "runs": runs})
}

// RequestRun enqueues a manual run of one job.
func (h *Handler) RequestRun(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[struct {
		Job string `json:"job"`
	}](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if !ValidJob(in.Job) {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_job",
			"job must be one of dump, drill, mirror, user_zip"))
		return
	}
	id, err := h.repo.Request(r.Context(), in.Job)
	if errors.Is(err, ErrRunPending) {
		httperr.Write(w, httperr.New(http.StatusConflict, "backup_run_pending",
			"a run of this job is already requested or running"))
		return
	}
	if err != nil {
		h.logger.Error("backup run request", "err", err, "job", in.Job)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if h.audit != nil {
		h.audit(r, in.Job)
	}
	// 202, not 201: nothing ran yet. The row is a request the agent claims on
	// its next poll — or never, on an instance without the backup profile,
	// which is exactly what the band's aging-requested warning surfaces.
	httperr.JSON(w, http.StatusAccepted, map[string]any{"id": id, "job": in.Job, "status": "requested"})
}

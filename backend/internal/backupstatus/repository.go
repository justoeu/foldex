// Package backupstatus is the backend's read side of the operational backup
// agent (ADR-43, SDD-OPS-BACKUP §10): the admin surface over backup_run.
//
// The agent (internal/backupagent) is the table's only real writer; this
// package reads history and summaries, and inserts exactly one thing — a
// 'requested' row for the agent to claim. The S3 credentials, the dump and the
// drill never pass through the web process.
package backupstatus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/backupagent"
)

// Jobs lists every job in the order the admin surface renders them. Reused
// from backupagent so this package cannot drift from the CHECK constraint in
// migration 000040.
var Jobs = []string{
	backupagent.JobDump,
	backupagent.JobDrill,
	backupagent.JobMirror,
	backupagent.JobUserZip,
}

// ValidJob reports whether name is one of the four jobs.
func ValidJob(name string) bool {
	for _, j := range Jobs {
		if j == name {
			return true
		}
	}
	return false
}

// ErrRunPending reports that the job already has a 'requested' or 'running'
// row — enqueueing a second would only pile up work the agent executes
// serially anyway. Semantic, transport-agnostic: the handler maps it to 409.
var ErrRunPending = errors.New("backupstatus: a run of this job is already requested or running")

// Run is one backup_run row as the admin surface serves it. Pointer fields are
// the columns that are NULL until (or unless) a phase fills them.
type Run struct {
	ID             int64          `json:"id"`
	Job            string         `json:"job"`
	Status         string         `json:"status"`
	ScheduledFor   time.Time      `json:"scheduled_for"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     *time.Time     `json:"finished_at"`
	ArtifactKey    *string        `json:"artifact_key"`
	ArtifactBytes  *int64         `json:"artifact_bytes"`
	ArtifactSHA256 *string        `json:"artifact_sha256"`
	ObjectsScanned *int64         `json:"objects_scanned"`
	ObjectsCopied  *int64         `json:"objects_copied"`
	BytesCopied    *int64         `json:"bytes_copied"`
	DrillOfRunID   *int64         `json:"drill_of_run_id"`
	LastError      *string        `json:"last_error"`
	Meta           map[string]any `json:"meta"`
}

// JobStatus is the per-job summary the status band renders at a glance. The
// drill's LastSuccess carries drill_of_run_id and the restored table counts in
// Meta — that row IS the "last drill" highlight.
type JobStatus struct {
	Job                 string `json:"job"`
	LastSuccess         *Run   `json:"last_success"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
}

// Repository reads backup_run for the admin surface.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const runColumns = `id, job, status, scheduled_for, started_at, finished_at,
	artifact_key, artifact_bytes, artifact_sha256,
	objects_scanned, objects_copied, bytes_copied,
	drill_of_run_id, last_error, meta`

func scanRun(row pgx.Row) (*Run, error) {
	r := &Run{}
	err := row.Scan(&r.ID, &r.Job, &r.Status, &r.ScheduledFor, &r.StartedAt, &r.FinishedAt,
		&r.ArtifactKey, &r.ArtifactBytes, &r.ArtifactSHA256,
		&r.ObjectsScanned, &r.ObjectsCopied, &r.BytesCopied,
		&r.DrillOfRunID, &r.LastError, &r.Meta)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Summary builds one JobStatus per job, in Jobs order.
//
// Two small queries per job rather than one clever statement: this renders an
// administration band a human opens occasionally, and the readable form is
// worth more than saving six round trips.
func (s *Repository) Summary(ctx context.Context) ([]JobStatus, error) {
	out := make([]JobStatus, 0, len(Jobs))
	for _, job := range Jobs {
		last, err := scanRun(s.pool.QueryRow(ctx, `
			SELECT `+runColumns+` FROM backup_run
			WHERE job = $1 AND status = 'succeeded'
			ORDER BY started_at DESC LIMIT 1`, job))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("backupstatus: last success for %s: %w", job, err)
		}
		fails, err := s.consecutiveFailures(ctx, job)
		if err != nil {
			return nil, err
		}
		out = append(out, JobStatus{Job: job, LastSuccess: last, ConsecutiveFailures: fails})
	}
	return out, nil
}

// consecutiveFailures delegates to the agent's own counter: the number the
// band shows MUST be the number the alert threshold compares against, and two
// hand-copied queries is exactly how they drift apart.
func (s *Repository) consecutiveFailures(ctx context.Context, job string) (int, error) {
	return backupagent.NewRunStore(s.pool).ConsecutiveFailures(ctx, job)
}

// ListRuns pages the history newest-first. before is a keyset cursor (the last
// id already shown; 0 means the head) — the table grows at its head, so an
// offset page would repeat rows as soon as the agent wrote anything between
// two requests. job filters when non-empty; the caller validates it.
func (s *Repository) ListRuns(ctx context.Context, job string, limit int, before int64) ([]Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+` FROM backup_run
		WHERE ($1 = '' OR job = $1)
		  AND ($2::bigint = 0 OR id < $2)
		ORDER BY id DESC
		LIMIT $3`, job, before, limit)
	if err != nil {
		return nil, fmt.Errorf("backupstatus: list runs: %w", err)
	}
	defer rows.Close()

	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("backupstatus: scan run: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backupstatus: list rows: %w", err)
	}
	return out, nil
}

// Request enqueues a manual run: one 'requested' row the agent claims on its
// next poll (~30s). Refused with ErrRunPending while the job already has a
// requested or running row — the retry policy for backups is "the next run",
// never a queue.
//
// The guard is a WHERE NOT EXISTS in the same statement, not an application
// check: two administrators clicking together race, and the single statement
// closes most of that window. It is deliberately not made fully serializable —
// backup_run_one_running_idx already guarantees the agent can never EXECUTE
// two at once, so the residual worst case of a photo-finish double click is a
// second requested row the agent drains one poll later.
func (s *Repository) Request(ctx context.Context, job string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, scheduled_for)
		SELECT $1, 'requested', now()
		WHERE NOT EXISTS (
			SELECT 1 FROM backup_run WHERE job = $1 AND status IN ('requested', 'running')
		)
		RETURNING id`, job).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrRunPending
	}
	if err != nil {
		return 0, fmt.Errorf("backupstatus: request run: %w", err)
	}
	return id, nil
}

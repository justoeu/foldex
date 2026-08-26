package backupagent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/pgerr"
)

// Job names, mirrored by the CHECK constraint in migration 000040.
const (
	JobDump    = "dump"
	JobMirror  = "mirror"
	JobUserZip = "user_zip"
	JobDrill   = "drill"
)

// ErrAlreadyRunning reports that another run of the same job holds the
// partial-unique 'running' slot. The caller skips the slot rather than
// queueing: the retry policy for backups is "the next scheduled run".
var ErrAlreadyRunning = errors.New("backupagent: a run of this job is already recorded as running")

// Normalized failure reasons — the only values ever written to
// backup_run.last_error. Raw tool output never lands there: pg_dump stderr can
// carry a DSN, and this column is rendered by the admin UI and mailed in
// alerts.
const (
	ReasonDumpFailed    = "pg_dump_failed"
	ReasonUserZipFailed = "user_zip_failed"
	ReasonEncryptFailed = "encrypt_failed"
	ReasonSpoolFailed   = "spool_failed"
	ReasonUploadFailed  = "upload_failed"
	ReasonPruneFailed   = "prune_failed"
	ReasonStaleClaim    = "stale_claim"
	ReasonShutdown      = "shutdown"
	ReasonLockBusy      = "lock_busy"

	ReasonDrillSourceFailed   = "drill_source_failed"
	ReasonDrillNoDump         = "drill_no_dump"
	ReasonDrillDownloadFailed = "drill_download_failed"
	ReasonDrillDigestMismatch = "drill_digest_mismatch"
	ReasonDrillDecryptFailed  = "drill_decrypt_failed"
	ReasonDrillRestoreFailed  = "drill_restore_failed"
	ReasonDrillCountsMismatch = "drill_counts_mismatch"

	ReasonRestoreInFlight  = "restore_in_flight"
	ReasonMirrorScanFailed = "mirror_scan_failed"
	ReasonMirrorCopyFailed = "mirror_copy_failed"
)

// RunStore is the agent's only writer of backup_run. The backend reads the
// table (admin surface, PR5) and inserts 'requested' rows; everything else is
// owned here.
type RunStore struct {
	pool  *pgxpool.Pool
	claim uuid.UUID // this agent instance's identity, stamped on every row it owns
}

func NewRunStore(pool *pgxpool.Pool) *RunStore {
	return &RunStore{pool: pool, claim: uuid.New()}
}

// Begin records a running row for job and returns its id. A unique-violation
// on backup_run_one_running_idx becomes ErrAlreadyRunning — persistence-level
// mutual exclusion against a second agent, complementing the advisory lock.
func (s *RunStore) Begin(ctx context.Context, job string, scheduledFor time.Time) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO backup_run (job, status, claim_token, scheduled_for)
		VALUES ($1, 'running', $2, $3)
		RETURNING id`, job, s.claim, scheduledFor).Scan(&id)
	if err != nil {
		if pgerr.UniqueConstraint(err) == "backup_run_one_running_idx" {
			return 0, ErrAlreadyRunning
		}
		return 0, fmt.Errorf("backupagent: begin run: %w", err)
	}
	return id, nil
}

// ClaimRequested promotes the oldest 'requested' row for job to running, CAS
// style. ok=false means there was nothing to claim OR the running slot is
// taken — both mean "not now".
func (s *RunStore) ClaimRequested(ctx context.Context, job string) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		UPDATE backup_run SET status = 'running', claim_token = $2, started_at = now()
		WHERE id = (
			SELECT id FROM backup_run
			WHERE job = $1 AND status = 'requested'
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id`, job, s.claim).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, nil
	case pgerr.UniqueConstraint(err) == "backup_run_one_running_idx":
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("backupagent: claim requested: %w", err)
	}
	return id, true, nil
}

// Artifact describes what a successful run produced. Jobs that ship one
// object (dump, user_zip) fill Key/Bytes/SHA256; the mirror fills Mirror
// instead — its product is a delta of many objects, and the counters are what
// the admin surface renders.
type Artifact struct {
	Key    string
	Bytes  int64
	SHA256 string
	Mirror *MirrorStats
}

// MirrorStats maps to backup_run's objects_scanned/objects_copied/bytes_copied.
type MirrorStats struct {
	ObjectsScanned int64
	ObjectsCopied  int64
	BytesCopied    int64
}

// Succeed finishes a run. artifact may be nil (drill); meta lands in
// the JSONB column (tool versions, per-phase durations, table counts).
func (s *RunStore) Succeed(ctx context.Context, id int64, artifact *Artifact, meta map[string]any) error {
	var key, sha *string
	var size *int64
	var scanned, copied, copiedBytes *int64
	if artifact != nil {
		if artifact.Key != "" {
			key, size, sha = &artifact.Key, &artifact.Bytes, &artifact.SHA256
		}
		if m := artifact.Mirror; m != nil {
			scanned, copied, copiedBytes = &m.ObjectsScanned, &m.ObjectsCopied, &m.BytesCopied
		}
	}
	if meta == nil {
		meta = map[string]any{}
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE backup_run
		SET status = 'succeeded', finished_at = now(),
		    artifact_key = $2, artifact_bytes = $3, artifact_sha256 = $4,
		    objects_scanned = $5, objects_copied = $6, bytes_copied = $7, meta = $8
		WHERE id = $1 AND claim_token = $9`,
		id, key, size, sha, scanned, copied, copiedBytes, meta, s.claim)
	if err != nil {
		return fmt.Errorf("backupagent: finish run: %w", err)
	}
	return nil
}

// Fail finishes a run with a normalized reason.
func (s *RunStore) Fail(ctx context.Context, id int64, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE backup_run
		SET status = 'failed', finished_at = now(), last_error = $2
		WHERE id = $1 AND claim_token = $3`, id, reason, s.claim)
	if err != nil {
		return fmt.Errorf("backupagent: fail run: %w", err)
	}
	return nil
}

// DumpRunRef identifies the dump run a drill validates: the artifact in the
// bucket, the ciphertext digest recorded for it, and the meta whose table
// counts the restored database is compared against.
type DumpRunRef struct {
	ID     int64
	Key    string
	SHA256 string
	Meta   map[string]any
}

// ErrNoDumpToDrill reports that no succeeded dump exists yet — there is
// nothing whose restorability could be proven.
var ErrNoDumpToDrill = errors.New("backupagent: no succeeded dump run with an artifact to drill")

// LatestSucceededDump picks the newest succeeded dump with an artifact.
// Always the newest, drilled before or not: re-validating is cheap and every
// run re-proves the bucket's bytes, not just the pipeline's memory of them
// (SDD-OPS-BACKUP §5.2).
func (s *RunStore) LatestSucceededDump(ctx context.Context) (*DumpRunRef, error) {
	ref := &DumpRunRef{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, artifact_key, COALESCE(artifact_sha256, ''), meta FROM backup_run
		WHERE job = $1 AND status = 'succeeded' AND artifact_key IS NOT NULL
		ORDER BY started_at DESC LIMIT 1`, JobDump).
		Scan(&ref.ID, &ref.Key, &ref.SHA256, &ref.Meta)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoDumpToDrill
	}
	if err != nil {
		return nil, fmt.Errorf("backupagent: latest dump: %w", err)
	}
	return ref, nil
}

// SetDrillSource stamps drill_of_run_id on the drill's own row the moment a
// source is picked — a drill that fails mid-pipeline must still record WHICH
// dump it was validating.
func (s *RunStore) SetDrillSource(ctx context.Context, runID, sourceRunID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE backup_run SET drill_of_run_id = $2
		WHERE id = $1 AND claim_token = $3`, runID, sourceRunID, s.claim)
	if err != nil {
		return fmt.Errorf("backupagent: link drill source: %w", err)
	}
	return nil
}

// LastSuccess returns the started_at of the most recent succeeded run of job,
// or the zero time when it never succeeded. Catch-up decides from this — the
// state lives in the database precisely so the container stays disposable.
func (s *RunStore) LastSuccess(ctx context.Context, job string) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT started_at FROM backup_run
		WHERE job = $1 AND status = 'succeeded'
		ORDER BY started_at DESC LIMIT 1`, job).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("backupagent: last success: %w", err)
	}
	return at, nil
}

// ConsecutiveFailures counts failed runs of job since its last success — the
// number the alert threshold compares against. Operational outcomes are
// excluded: a deploy mid-run (shutdown), a sibling agent holding the lock
// (lock_busy), a janitor-expired corpse (stale_claim) or a per-user restore
// occupying the bucket (restore_in_flight) say nothing about whether backups
// WORK, and counting them would page the operator for a restart plus one real
// failure.
func (s *RunStore) ConsecutiveFailures(ctx context.Context, job string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM backup_run
		WHERE job = $1 AND status = 'failed'
		  AND last_error NOT IN ($2, $3, $4, $5)
		  AND started_at > COALESCE(
			(SELECT max(started_at) FROM backup_run WHERE job = $1 AND status = 'succeeded'),
			'-infinity')`, job, ReasonShutdown, ReasonLockBusy, ReasonStaleClaim, ReasonRestoreInFlight).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("backupagent: consecutive failures: %w", err)
	}
	return n, nil
}

// ExpireStale flips running rows older than ttl to failed('stale_claim').
// Without it, an agent that died mid-run holds backup_run_one_running_idx
// forever and the job never runs again — the analogue of the outbox claim TTL.
// It expires OTHER agents' stale rows too, deliberately: the successor cleans
// up after the dead.
func (s *RunStore) ExpireStale(ctx context.Context, ttl time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE backup_run
		SET status = 'failed', finished_at = now(), last_error = $1
		WHERE status = 'running' AND started_at < now() - $2::interval`,
		ReasonStaleClaim, ttl.String())
	if err != nil {
		return 0, fmt.Errorf("backupagent: expire stale: %w", err)
	}
	return tag.RowsAffected(), nil
}

// SchemaVersion returns the migration number the database is at. The agent
// gates its boot on this (>= 40) instead of running migrations itself: the
// backend stays the single owner of schema changes.
func (s *RunStore) SchemaVersion(ctx context.Context) (int64, error) {
	var v int64
	var dirty bool
	if err := s.pool.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&v, &dirty); err != nil {
		return 0, fmt.Errorf("backupagent: schema version: %w", err)
	}
	if dirty {
		return 0, errors.New("backupagent: schema_migrations is dirty — resolve the failed migration first")
	}
	return v, nil
}

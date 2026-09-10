package backupstatus

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
)

// MaxDownloadsPerAdmin is the ceiling from INV-187: how many times ONE
// administrator may pull ONE artifact.
//
// Per administrator, not per instance, by the owner's decision: a shared pool
// would let the first admin to hit a network hiccup spend everyone else's
// budget, and the audit trail already names who pulled what. Three is enough
// for a retry and a colleague's second attempt, and small enough that a
// scripted exfiltration runs out.
const MaxDownloadsPerAdmin = 3

// ErrDownloadBudgetSpent is returned when the caller has no downloads left for
// this artifact. It is a transport-agnostic sentinel: the handler owns the
// status and the code (§7).
var ErrDownloadBudgetSpent = errors.New("backupstatus: download budget spent for this artifact")

// DownloadBudget is what the screen renders beside the button.
type DownloadBudget struct {
	Used      int `json:"used"`
	Limit     int `json:"limit"`
	Available int `json:"available"`
}

// DownloadBudgetFor reports what this caller has left for this artifact.
func (r *Repository) DownloadBudgetFor(ctx context.Context, runID int64, uid authctx.UserID, key string) (DownloadBudget, error) {
	var used int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM backup_download
		WHERE run_id = $1 AND user_id = $2 AND object_key = $3`,
		runID, int64(uid), key).Scan(&used)
	if err != nil {
		return DownloadBudget{}, fmt.Errorf("backupstatus: download budget: %w", err)
	}
	available := MaxDownloadsPerAdmin - used
	if available < 0 {
		available = 0
	}
	return DownloadBudget{Used: used, Limit: MaxDownloadsPerAdmin, Available: available}, nil
}

/*
ReserveDownload spends one of the caller's three and returns the reservation id.

**The advisory lock is what makes the ceiling real.** Folding the count into
the INSERT (`INSERT ... SELECT ... WHERE (SELECT count) < n`) LOOKS atomic and
is not: under READ COMMITTED each statement takes its own snapshot, so two
requests fired in parallel both read "two used" — neither sees the other's
uncommitted row — and both insert. The fourth download the ceiling exists to
refuse costs an attacker a two-line script. A single SQL statement is not a
critical section.

So the count and the insert run inside ONE transaction behind
`pg_advisory_xact_lock`, keyed on the exact (run, user, artifact) triple the
budget is about — the same tool the agent uses to serialize jobs (lock.go), at
this row's granularity rather than the instance's. The lock releases with the
transaction, including on rollback.

The reservation is spent BEFORE any byte moves. Charging on completion instead
would let a caller take 99% of the artifact and abort, forever: whoever holds
99% of a dump holds the dump.
*/
func (r *Repository) ReserveDownload(ctx context.Context, runID int64, uid authctx.UserID, key string) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("backupstatus: reserve download: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// hashtextextended over the triple: two different artifacts, or two
	// different administrators, must not wait on each other.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("backup_download:%d:%d:%s", runID, int64(uid), key)); err != nil {
		return 0, fmt.Errorf("backupstatus: reserve download lock: %w", err)
	}

	var used int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM backup_download
		WHERE run_id = $1 AND user_id = $2 AND object_key = $3`,
		runID, int64(uid), key).Scan(&used); err != nil {
		return 0, fmt.Errorf("backupstatus: reserve download count: %w", err)
	}
	if used >= MaxDownloadsPerAdmin {
		return 0, ErrDownloadBudgetSpent
	}

	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO backup_download (run_id, user_id, object_key)
		VALUES ($1, $2, $3) RETURNING id`,
		runID, int64(uid), key).Scan(&id); err != nil {
		return 0, fmt.Errorf("backupstatus: reserve download: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("backupstatus: reserve download commit: %w", err)
	}
	return id, nil
}

/*
ReleaseDownload gives the reservation back, and it has exactly one caller.

The budget is about BYTES THAT LEFT THE INSTANCE, so a stream that dies at 99%
keeps its charge — whoever holds 99% of a dump holds the dump. But a bridge
that never answered moved nothing, and charging for it is not anti-abuse: with
a ceiling of three, an unreachable agent costs the owner three attempts and
then locks them out of that artifact PERMANENTLY, with no way back except a
hand-written DELETE. The feature would fail closed against its own operator for
a misconfiguration they are in the middle of fixing.

The refund is therefore only reachable before io.Copy starts. It cannot be used
to retry for free, because a retry that produces bytes is charged like any
other.
*/
func (r *Repository) ReleaseDownload(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM backup_download WHERE id = $1 AND bytes_sent IS NULL`, id)
	if err != nil {
		return fmt.Errorf("backupstatus: release download: %w", err)
	}
	return nil
}

// CompleteDownload stamps what actually reached the caller.
//
// It never releases the reservation: a download that failed halfway still
// moved bytes, and the budget is about bytes that left the instance, not about
// requests that returned 200. This only lets the trail answer "did it arrive
// whole?".
func (r *Repository) CompleteDownload(ctx context.Context, id int64, bytesSent int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE backup_download SET completed = true, bytes_sent = $2 WHERE id = $1`, id, bytesSent)
	if err != nil {
		return fmt.Errorf("backupstatus: complete download: %w", err)
	}
	return nil
}

// ArtifactKeyForRun returns the object a run shipped, or "" when it shipped
// none. A mirror or user_zip run legitimately has no single key.
func (r *Repository) ArtifactKeyForRun(ctx context.Context, runID int64) (string, error) {
	var key *string
	err := r.pool.QueryRow(ctx, `SELECT artifact_key FROM backup_run WHERE id = $1`, runID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrRunNotFound
	}
	if err != nil {
		return "", fmt.Errorf("backupstatus: artifact key: %w", err)
	}
	if key == nil {
		return "", nil
	}
	return *key, nil
}

// ErrRunNotFound is the semantic error for a backup_run id that is not there.
var ErrRunNotFound = errors.New("backupstatus: run not found")

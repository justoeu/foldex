package backupagent

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/backup"
)

// acquireJobLock takes the agent's session-level advisory lock, returning a
// release func. ok=false means another agent holds it — skip the slot, do not
// wait: a queued backup behind a stuck backup is two stuck backups.
//
// Session locks (not xact locks) because a job spans many transactions, and a
// dropped connection releases them — which is why the persistence-level unique
// index needs the janitor as its own safety net, not this lock.
func acquireJobLock(ctx context.Context, pool *pgxpool.Pool) (release func(), ok bool, err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("backupagent: acquire conn for lock: %w", err)
	}
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`,
		backup.InstanceBackupAdvisoryLockKey).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("backupagent: try advisory lock: %w", err)
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	release = func() {
		// Best-effort on a background context: the job's ctx may already be
		// cancelled (shutdown), and releasing the conn drops the session lock
		// anyway — the explicit unlock just does it without churning the pool.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`,
			backup.InstanceBackupAdvisoryLockKey)
		conn.Release()
	}
	return release, true, nil
}

// restoreInFlight probes RestoreAdvisoryLockKey (acquire-and-release).
//
// No production caller in PR1, deliberately: it ships now because it is the
// other half of the coordination contract INV-104 demands of the bucket-
// reading jobs (mirror in PR3, user_zip in PR4), and shipping the probe with
// its integration test in the same change as the lock constants keeps the
// contract reviewable in one place. The dump does not need it — a restore is
// a single transaction and MVCC hands pg_dump a consistent snapshot.
func restoreInFlight(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	// One pinned connection for the whole probe: advisory locks are
	// per-session, so acquire and unlock through the pool's load balancer
	// would land on different sessions and the unlock would silently fail.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("backupagent: acquire conn for probe: %w", err)
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`,
		backup.RestoreAdvisoryLockKey).Scan(&got); err != nil {
		return false, fmt.Errorf("backupagent: probe restore lock: %w", err)
	}
	if got {
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`,
			backup.RestoreAdvisoryLockKey); err != nil {
			return false, fmt.Errorf("backupagent: release restore probe: %w", err)
		}
		return false, nil
	}
	return true, nil
}

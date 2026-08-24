package roleperm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

// Repository owns the stored grants and the snapshot the gate reads.
//
// The snapshot exists because Can is on the authorization path of EVERY
// request: a query per permission check would put a round trip in front of
// every API call, and a cache miss during a database blip would have to choose
// between denying everything and allowing everything. A snapshot chooses
// neither — it keeps answering the last thing the database said.
type Repository struct {
	pool *pgxpool.Pool

	mu      sync.RWMutex
	current Grants
}

// NewRepository returns a repository already holding the COMPILED matrix.
//
// Not an empty one: Load can fail, and a gate consulting an empty snapshot
// would refuse every request on the instance including the ones needed to
// diagnose it. Starting from the compiled matrix means the worst case of an
// unreachable database is the behaviour foldex had before any of this was
// configurable.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, current: Default()}
}

// Grants returns the current snapshot.
func (r *Repository) Grants() Grants {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

// Can implements the authorization gate's reader.
func (r *Repository) Can(role authctx.Role, p authctx.Permission) bool {
	return r.Grants().Can(role, p)
}

// Load reads the stored rows and replaces the snapshot.
//
// A read error leaves the previous snapshot in place and is returned for the
// caller to log. Replacing it with the compiled matrix on every failure would
// silently restore permissions an owner had deliberately revoked.
func (r *Repository) Load(ctx context.Context) error {
	stored, err := r.fetch(ctx)
	if err != nil {
		return err
	}
	resolved := Resolve(stored)
	r.mu.Lock()
	r.current = resolved
	r.mu.Unlock()
	return nil
}

func (r *Repository) fetch(ctx context.Context) (map[authctx.Role][]authctx.Permission, error) {
	rows, err := r.pool.Query(ctx, `SELECT role, permission FROM role_permission`)
	if err != nil {
		return nil, fmt.Errorf("roleperm fetch: %w", err)
	}
	defer rows.Close()

	out := make(map[authctx.Role][]authctx.Permission, len(authctx.AllRoles))
	for rows.Next() {
		var role, perm string
		if err := rows.Scan(&role, &perm); err != nil {
			return nil, fmt.Errorf("roleperm scan: %w", err)
		}
		out[authctx.Role(role)] = append(out[authctx.Role(role)], authctx.Permission(perm))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("roleperm rows: %w", err)
	}
	return out, nil
}

// Set replaces one role's editable grants and refreshes the snapshot.
//
// Delete-then-insert inside ONE transaction, because the two halves are one
// statement of intent: a failure between them would leave the role holding
// whichever permissions happened to be inserted first. The locked entries are
// never written — Resolve puts them back from the compiled matrix on every
// read, so storing them would create a second source of truth for the one part
// of the matrix that must not have one.
func (r *Repository) Set(ctx context.Context, caller authctx.Role, target authctx.Role, want []authctx.Permission) error {
	if err := ValidateWrite(r.Grants(), caller, target, want); err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("roleperm begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A transaction-scoped advisory lock, keyed by the ROLE.
	//
	// DELETE-then-INSERT under READ COMMITTED loses a revocation: a second
	// transaction snapshots before the first commits, so rows the first
	// inserted are invisible to its DELETE and survive it. An owner sending []
	// concurrently with an admin sending ["content.write"] leaves the role
	// holding content.write — precisely the merge of two intents that sending
	// the FULL set is supposed to make impossible.
	//
	// An advisory lock rather than SERIALIZABLE because there is nothing to
	// retry here: these writes are rare, human-paced and mutually exclusive by
	// nature, so the second one waiting is the behaviour we want, and a
	// serialization failure would surface to the owner as a 500 they would
	// simply repeat.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('foldex.role_permission'), hashtext($1))`,
		string(target)); err != nil {
		return fmt.Errorf("roleperm lock: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM role_permission WHERE role = $1`, string(target)); err != nil {
		return fmt.Errorf("roleperm clear: %w", err)
	}
	if len(want) > 0 {
		// One statement, not one per permission: ten round trips inside a
		// transaction for a set this small is the pattern CLAUDE.md already
		// refuses for slug allocation.
		perms := make([]string, 0, len(want))
		for _, p := range want {
			perms = append(perms, string(p))
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_permission (role, permission)
			 SELECT $1, p FROM unnest($2::text[]) AS p
			 ON CONFLICT DO NOTHING`, string(target), perms); err != nil {
			return fmt.Errorf("roleperm insert: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("roleperm commit: %w", err)
	}
	// Best effort: the write committed, so a failed refresh means this process
	// serves a stale snapshot until StartReloading's next tick — never that the
	// change was lost.
	_ = r.Load(ctx)
	return nil
}

// DefaultReloadInterval is how long a change takes to reach a replica that did
// not perform it.
//
// Short because it bounds a REVOCATION: the value is how long a role keeps a
// permission its owner already took away, on any process other than the one
// that handled the write. The query is a nineteen-row sequential scan against
// a table the primary key already covers, so the cost of being wrong in the
// generous direction is nil.
const DefaultReloadInterval = 30 * time.Second

// StartReloading refreshes the snapshot until ctx is cancelled.
//
// Without it there is no periodic Load at all, and two things follow that the
// code claimed were handled. A second backend replica never sees a revocation,
// because the in-process refresh after Set runs only where the write landed.
// And on the process that DID write, that refresh is best-effort — if it fails
// the snapshot stays pre-revocation forever, which fails OPEN: the very
// direction the rest of this package is built to avoid.
//
// A ticker rather than LISTEN/NOTIFY: notify needs a dedicated connection and a
// reconnect policy, and its failure mode is silence — a dropped listener looks
// exactly like an instance nobody is editing. A ticker that fails is a log line
// and a retry thirty seconds later.
// The returned channel closes when the goroutine has exited, so a caller —
// today only a test — can observe the stop rather than infer it from a
// goroutine count the pool and the driver also move.
func (r *Repository) StartReloading(ctx context.Context, interval time.Duration, log *slog.Logger) <-chan struct{} {
	if interval <= 0 {
		interval = DefaultReloadInterval
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := r.Load(ctx); err != nil && ctx.Err() == nil {
					// Logged, never fatal: the previous snapshot stays in
					// force, which is the last thing the database actually
					// said rather than a guess in either direction.
					log.Warn("role permissions reload", "err", err)
				}
			}
		}
	}()
	return done
}

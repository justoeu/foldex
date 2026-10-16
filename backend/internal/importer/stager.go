package importer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/pgerr"
)

// Stager owns the import's persistence: the preview/validate reads and the
// one transaction that applies a staged import. The HTTP handler keeps only
// admission control and post-commit side effects (worker enqueue), so no
// delivery file holds the persistence driver — same shape as every sibling
// feature's repository.
type Stager struct {
	pool *pgxpool.Pool
}

func NewStager(pool *pgxpool.Pool) *Stager { return &Stager{pool: pool} }

func (s *Stager) Validate(ctx context.Context, uid authctx.UserID, items []Item) (ValidationReport, error) {
	return Validate(ctx, s.pool, uid, items)
}

// ApplyWithMode runs the staged import inside one transaction and returns the
// import counts plus the warnings collected before commit; the caller owns
// post-commit side effects for the returned fresh link ids.
// maxSlugRaceAttempts bounds the whole-transaction retry when a concurrent
// create grabs a global slug between the taken-slug preload and the insert.
const maxSlugRaceAttempts = 3

func isSlugRace(err error) bool {
	switch pgerr.UniqueConstraint(err) {
	case "link_slug_unique", "note_slug_unique":
		return true
	}
	return false
}

func (s *Stager) ApplyWithMode(ctx context.Context, uid authctx.UserID, items []Item, mode importMode, seed *jsonSeed) (imported, skipped, wiped int, warnings []string, freshIDs []int64, err error) {
	if err := validateImportClickBudget(items); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	for attempt := 1; ; attempt++ {
		imported, skipped, wiped, warnings, freshIDs, err = s.applyWithModeOnce(ctx, uid, items, mode, seed)
		if err == nil || !isSlugRace(err) || attempt >= maxSlugRaceAttempts {
			return imported, skipped, wiped, warnings, freshIDs, err
		}
	}
}

func (s *Stager) applyWithModeOnce(ctx context.Context, uid authctx.UserID, items []Item, mode importMode, seed *jsonSeed) (imported, skipped, wiped int, warnings []string, freshIDs []int64, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("begin import tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Serialize against this user's other imports/writes for the staging
	// phase; cross-tenant slug races are covered by the retry above.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, int64(uid)); err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("lock importing user: %w", err)
	}

	imported, skipped, wiped, warnings, freshIDs, err = applyStagedImport(ctx, tx, uid, items, mode, seed)
	if err != nil {
		return 0, 0, 0, nil, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return imported, skipped, wiped, warnings, freshIDs, fmt.Errorf("commit import: %w", err)
	}
	return imported, skipped, wiped, warnings, freshIDs, nil
}

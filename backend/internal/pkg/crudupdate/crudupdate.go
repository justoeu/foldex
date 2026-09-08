// Package crudupdate owns the partial-UPDATE skeleton shared by the links and
// notes repositories: SET-list building with a single placeholder counter, the
// owner-scoped + If-Match WHERE, and the RowsAffected==0
// stale-write-vs-missing disambiguation. The optimistic-concurrency and
// ownership rules are security-relevant and must stay in lockstep between the
// two entities — before this package existed, each carried a private copy and
// they had already diverged in detail.
package crudupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
)

// SetBuilder accumulates "col = $N" assignments with one shared placeholder
// counter. Owning the counter here is the point: inserting a field mid-sequence
// used to renumber every $N downstream by hand, silently, across two packages.
type SetBuilder struct {
	sets []string
	args []any
}

// Set appends "col = $N" for val and returns the placeholder position, so
// callers can reference the same argument in companion expressions (links'
// change-check reset conditions compare against the value just written).
func (b *SetBuilder) Set(col string, val any) int {
	b.sets = append(b.sets, fmt.Sprintf("%s = $%d", col, len(b.args)+1))
	b.args = append(b.args, val)
	return len(b.args)
}

// Raw appends a SET fragment that introduces no new arguments (updated_at =
// now(), the CASE WHEN reset fan-out).
func (b *SetBuilder) Raw(clause string) { b.sets = append(b.sets, clause) }

// Empty reports whether any column assignment was made.
func (b *SetBuilder) Empty() bool { return len(b.sets) == 0 }

// Request parameterizes the shared executor per entity.
type Request struct {
	// Entity names the table — a compile-time literal ("link"/"note"), never
	// client input; it flows into the SQL text.
	Entity string
	UID    authctx.UserID
	ID     int64
	// IfMatch enables optimistic concurrency: the UPDATE also requires
	// row.updated_at equality; zero rows affected then means either a stale
	// write (row exists) or a missing row, disambiguated by the probe below.
	IfMatch *time.Time
	// StaleErr is the package-local sentinel returned for the stale case.
	StaleErr error
	// Translate maps unique-constraint violations to package sentinels; nil
	// return falls through to the generic wrapped error.
	Translate func(error) error
}

// Exec runs the partial UPDATE the builder assembled: owner-scoped WHERE,
// optional updated_at compare, and — when zero rows are affected — the
// exists-probe that tells a stale write from a missing row.
func Exec(ctx context.Context, tx pgx.Tx, req Request, b *SetBuilder) error {
	b.Raw("updated_at = now()")
	args := append(b.args, int64(req.UID), req.ID)
	where := fmt.Sprintf("WHERE user_id = $%d AND id = $%d", len(args)-1, len(args))
	if req.IfMatch != nil {
		args = append(args, *req.IfMatch)
		where += fmt.Sprintf(" AND updated_at = $%d", len(args))
	}
	q := fmt.Sprintf("UPDATE %s SET %s %s", req.Entity, strings.Join(b.sets, ", "), where)
	ct, err := tx.Exec(ctx, q, args...)
	if err != nil {
		if mapped := req.Translate(err); mapped != nil {
			return mapped
		}
		return fmt.Errorf("update %s: %w", req.Entity, err)
	}
	if ct.RowsAffected() == 0 {
		if req.IfMatch != nil {
			exists, err := rowExists(ctx, tx, req.Entity, req.UID, req.ID)
			if err != nil {
				return err
			}
			if exists {
				return req.StaleErr
			}
		}
		return domainerr.ErrNotFound
	}
	return nil
}

// AssertOwned reports domainerr.ErrNotFound unless uid owns the row. It is the
// tag-only-PATCH ownership guard: when a PATCH carries no column writes, no
// owner-scoped UPDATE ran, and tag writes must not proceed on a row the caller
// does not own. table is a compile-time literal, never client input.
func AssertOwned(ctx context.Context, tx pgx.Tx, table string, uid authctx.UserID, id int64) error {
	var locked int64
	err := tx.QueryRow(ctx,
		fmt.Sprintf("SELECT id FROM %s WHERE user_id = $1 AND id = $2 FOR NO KEY UPDATE", table),
		int64(uid), id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return domainerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check %s owner: %w", table, err)
	}
	return nil
}

func rowExists(ctx context.Context, tx pgx.Tx, table string, uid authctx.UserID, id int64) (bool, error) {
	var exists bool
	if err := tx.QueryRow(ctx,
		fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE user_id = $1 AND id = $2)", table),
		int64(uid), id).Scan(&exists); err != nil {
		return false, fmt.Errorf("check %s exists: %w", table, err)
	}
	return exists, nil
}

package notes

// System-scoped repository methods: queries that legitimately run WITHOUT a
// user_id predicate. Today that is only the public, session-less GET
// /n/{id-or-slug} route, which has no tenant to scope by.
//
// A `FROM note` with no user_id predicate anywhere else is a bug; a CI grep
// enforces it. See docs/SDD-AUTH-RBAC.md §8.2.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/folders"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/publictarget"
	"foldex/internal/tags"
)

// SystemViewAndResolve resolves id-or-slug and logs a click_log row in the same
// tx, mirroring links.ClickAndResolve(BySlug) — used by GET /n/{id-or-slug}.
func (r *Repository) SystemViewAndResolve(ctx context.Context, idOrSlug string) (Note, error) {
	return publictarget.Resolve(
		ctx, idOrSlug, true,
		r.SystemViewAndResolveByID,
		r.SystemViewAndResolveBySlug,
	)
}

func (r *Repository) SystemViewAndResolveByID(ctx context.Context, id int64) (Note, error) {
	return r.systemViewAndResolveWhere(ctx, "n.id = $1", id)
}

func (r *Repository) SystemViewAndResolveBySlug(ctx context.Context, slug string) (Note, error) {
	return r.systemViewAndResolveWhere(ctx, "n.slug = $1", slug)
}

func (r *Repository) systemViewAndResolveWhere(ctx context.Context, where string, arg any) (Note, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Note{}, fmt.Errorf("begin view tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var id int64
	var owner int64
	// Public /n must 404 for notes inside password-protected folders.
	err = tx.QueryRow(ctx, `
        SELECT n.id, n.user_id FROM note n
        WHERE `+where+` AND `+folders.SQLNotInLockedFolder("n"), arg).Scan(&id, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, domainerr.ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("resolve note: %w", err)
	}

	// Owner from the resolved row — /n/ is public, there is no session here.
	if _, err := tx.Exec(ctx,
		`INSERT INTO click_log (entity_kind, entity_id, user_id) VALUES ('note', $1, $2)`,
		id, owner); err != nil {
		return Note{}, fmt.Errorf("insert click_log: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Note{}, fmt.Errorf("commit view tx: %w", err)
	}
	return r.systemGet(ctx, id)
}

// systemGet loads a note without an ownership predicate, for the public route.
// Tags are deliberately left empty: /n/{slug} renders the note body, and
// resolving chips would mean a second unscoped join for data the page does not
// show.
func (r *Repository) systemGet(ctx context.Context, id int64) (Note, error) {
	var n Note
	err := scanNote(r.pool.QueryRow(ctx, `SELECT `+noteDetailColumns+noteFrom+` WHERE n.id = $1`, id), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, domainerr.ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("system get note: %w", err)
	}
	n.Tags = []tags.Chip{}
	return n, nil
}

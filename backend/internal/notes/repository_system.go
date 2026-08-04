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
	"foldex/internal/links"
	"foldex/internal/pkg/httperr"
)

// SystemViewAndResolve resolves id-or-slug and logs a click_log row in the same
// tx, mirroring links.ClickAndResolve(BySlug) — used by GET /n/{id-or-slug}.
func (r *Repository) SystemViewAndResolve(ctx context.Context, idOrSlug string) (Note, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Note{}, fmt.Errorf("begin view tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var id int64
	where, arg := "n.slug = $1", any(idOrSlug)
	if n, ok := parsePositiveID(idOrSlug); ok {
		where, arg = "n.id = $1", any(n)
	}
	// Public /n must 404 for notes inside password-protected folders.
	err = tx.QueryRow(ctx, `
        SELECT n.id FROM note n
        WHERE `+where+` AND `+folders.SQLNotInLockedFolder("n"), arg).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, httperr.ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("resolve note: %w", err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO click_log (entity_kind, entity_id) VALUES ('note', $1)`, id); err != nil {
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
		return Note{}, httperr.ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("system get note: %w", err)
	}
	n.Tags = []links.Tag{}
	return n, nil
}

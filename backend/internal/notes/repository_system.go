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
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/clicklog"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/pkg/publictarget"
	"foldex/internal/tags"
)

// SystemViewAndResolve resolves id-or-slug and logs a click_log row in the same
// tx, mirroring links.ClickAndResolve(BySlug) — used by GET /n/{id-or-slug}.
//
// viewer is the resolved principal of the request (0 when anonymous): the note
// renders when it is opted in via is_public OR when the viewer owns it.
func (r *Repository) SystemViewAndResolve(ctx context.Context, idOrSlug string, viewer authctx.UserID) (Note, error) {
	return publictarget.Resolve(
		ctx, idOrSlug, true,
		func(ctx context.Context, id int64) (Note, error) {
			return r.SystemViewAndResolveByID(ctx, id, viewer)
		},
		func(ctx context.Context, slug string) (Note, error) {
			return r.SystemViewAndResolveBySlug(ctx, slug, viewer)
		},
	)
}

func (r *Repository) SystemViewAndResolveByID(ctx context.Context, id int64, viewer authctx.UserID) (Note, error) {
	return r.systemViewAndResolveWhere(ctx, viewer, "n.id = $1", id)
}

func (r *Repository) SystemViewAndResolveBySlug(ctx context.Context, slug string, viewer authctx.UserID) (Note, error) {
	return r.systemViewAndResolveWhere(ctx, viewer, "n.slug = $1", slug)
}

func (r *Repository) systemViewAndResolveWhere(ctx context.Context, viewer authctx.UserID, where string, arg any) (Note, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Note{}, fmt.Errorf("begin view tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// SEC-SEN-001: a title-derived slug is guessable, so it cannot be the
	// only thing standing between a note and every anonymous reader. The
	// page is a capability the owner grants (is_public) or uses themselves
	// (owner session, mounted optional on /n/). Locked folders stay a
	// further AND — opting a note in does not punch through a folder
	// password.
	visibility := "n.is_public"
	args := []any{arg}
	if viewer != 0 {
		visibility = "(n.is_public OR n.user_id = $2)"
		args = append(args, int64(viewer))
	}

	var id int64
	var owner int64
	err = tx.QueryRow(ctx, `
        SELECT n.id, n.user_id FROM note n
        WHERE `+where+` AND `+visibility+` AND `+folders.SQLNotInLockedFolder("n"), args...).Scan(&id, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, domainerr.ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("resolve note: %w", err)
	}

	// The same coalescing gate the link redirect consults — see
	// internal/pkg/clickctx. Absent gate means record, so nothing but the
	// public HTTP path changes behaviour, and a suppressed view still RENDERS:
	// only the click row is skipped. Owner from the resolved row — /n/ is
	// public, there is no session here.
	if err := clicklog.Record(ctx, tx, "note", id, owner); err != nil {
		return Note{}, err
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

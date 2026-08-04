package backup

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	slugpkg "foldex/internal/pkg/slug"
)

// uniqueLinkSlug returns the original slug if free, else the original with
// `-2`, `-3`, … suffix. Falls back to slugify(title) when the snapshot has
// an empty/missing slug.
//
// The existence check is deliberately NOT owner-scoped: link.slug stays
// globally unique after migration 000017 because /go/{slug} resolves with no
// session. A slug taken by ANOTHER tenant must still push this one to `-2`.
// Do not "fix" this by adding user_id — see docs/SDD-AUTH-RBAC.md §10.3.
func uniqueLinkSlug(ctx context.Context, tx pgx.Tx, s, title string) (string, error) {
	base := s
	if base == "" {
		base = slugpkg.Slugify(title)
		if base == "" {
			base = "link-restored"
		}
	}
	return slugpkg.UniqueAvailable(ctx, base, func(ctx context.Context, candidate string) (bool, error) {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM link WHERE slug = $1)`, candidate).Scan(&exists)
		return exists, err
	})
}

// uniqueNoteSlug is the note-table sibling of uniqueLinkSlug — and, like it,
// checks GLOBALLY on purpose (/n/{slug} is public).
func uniqueNoteSlug(ctx context.Context, tx pgx.Tx, s, title string) (string, error) {
	base := s
	if base == "" {
		base = slugpkg.Slugify(title)
		if base == "" {
			base = "note-restored"
		}
	}
	return slugpkg.UniqueAvailable(ctx, base, func(ctx context.Context, candidate string) (bool, error) {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM note WHERE slug = $1)`, candidate).Scan(&exists)
		return exists, err
	})
}

// uniqueTagName returns `base` if free, else `base (2)`, `base (3)`, ...
//
// Unlike the slug helpers above, this one IS owner-scoped: tag.name became
// UNIQUE (user_id, name) in migration 000017, so another tenant owning the same
// name is irrelevant here. The asymmetry with uniqueLinkSlug is intentional.
func uniqueTagName(ctx context.Context, tx pgx.Tx, uid authctx.UserID, base string) (string, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tag WHERE user_id=$2 AND name=$1)`, base, int64(uid)).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return base, nil
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)", base, i)
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tag WHERE user_id=$2 AND name=$1)`, candidate, int64(uid)).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("uniqueTagName: exhausted attempts for %q", base)
}

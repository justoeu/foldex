package backup

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	slugpkg "foldex/internal/pkg/slug"
)

// uniqueLinkSlug returns the original slug if free, else the original with
// `-2`, `-3`, … suffix. Falls back to slugify(title) when the snapshot has
// an empty/missing slug.
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

// uniqueNoteSlug is the note-table sibling of uniqueLinkSlug.
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
func uniqueTagName(ctx context.Context, tx pgx.Tx, base string) (string, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tag WHERE name=$1)`, base).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return base, nil
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)", base, i)
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tag WHERE name=$1)`, candidate).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("uniqueTagName: exhausted attempts for %q", base)
}

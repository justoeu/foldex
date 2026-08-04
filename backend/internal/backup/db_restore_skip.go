package backup

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
)

// ────────────────────────────────────────────────────────────────────────────
// Skip mode.

func restoreSkip(ctx context.Context, tx pgx.Tx, uid authctx.UserID, snap *Snapshot) (Counts, Counts, idMapping, error) {
	var inserted, skipped Counts
	m := newIDMapping()

	for _, t := range snap.Tags {
		var newID int64
		err := tx.QueryRow(ctx, `
            INSERT INTO tag (user_id, name, color, icon, created_at)
            VALUES ($1,$2,$3,$4,$5)
            ON CONFLICT (user_id, name) DO NOTHING
            RETURNING id`,
			int64(uid), t.Name, t.Color, t.Icon, t.CreatedAt).Scan(&newID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Already exists — fetch the existing id for the mapping.
			if err2 := tx.QueryRow(ctx, `SELECT id FROM tag WHERE user_id=$2 AND name=$1`, t.Name, int64(uid)).Scan(&newID); err2 != nil {
				return inserted, skipped, m, fmt.Errorf("fetch existing tag %q: %w", t.Name, err2)
			}
			skipped.Tags++
		} else if err != nil {
			return inserted, skipped, m, fmt.Errorf("insert tag %q: %w", t.Name, err)
		} else {
			inserted.Tags++
		}
		m.tagMap[t.ID] = newID
	}

	for _, f := range topoSortFolders(snap.Folders) {
		if _, err := insertFolderMapped(ctx, tx, uid, &m, f); err != nil {
			return inserted, skipped, m, err
		}
		inserted.Folders++
	}

	for _, l := range snap.Links {
		folderID := mapOptionalID(m.folderMap, l.FolderID)
		// Slug: try to keep the original; if it collides, uniquify. URL
		// collision still wins via ON CONFLICT (url).
		slug, err := uniqueLinkSlug(ctx, tx, l.Slug, l.Title)
		if err != nil {
			return inserted, skipped, m, err
		}
		var newID int64
		err = tx.QueryRow(ctx, `
            INSERT INTO link (user_id, url, title, slug, description, favicon_url, og_image_url,
                              pinned, preview_status, preview_error, folder_id,
                              created_at, updated_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
            ON CONFLICT (user_id, url) DO NOTHING
            RETURNING id`,
			int64(uid), l.URL, l.Title, slug, l.Description, l.FaviconURL, l.OGImageURL,
			l.Pinned, l.PreviewStatus, l.PreviewError, folderID, l.CreatedAt, l.UpdatedAt).Scan(&newID)
		if errors.Is(err, pgx.ErrNoRows) {
			if err2 := tx.QueryRow(ctx, `SELECT id FROM link WHERE user_id=$2 AND url=$1`, l.URL, int64(uid)).Scan(&newID); err2 != nil {
				return inserted, skipped, m, fmt.Errorf("fetch existing link: %w", err2)
			}
			skipped.Links++
		} else if err != nil {
			return inserted, skipped, m, fmt.Errorf("insert link: %w", err)
		} else {
			inserted.Links++
		}
		m.linkMap[l.ID] = newID
	}

	// Notes: always insert fresh (no content-identity key); slug uniquified.
	for _, n := range snap.Notes {
		if _, err := insertNoteMapped(ctx, tx, uid, &m, n); err != nil {
			return inserted, skipped, m, err
		}
		inserted.Notes++
	}

	if err := attachPolymorphicTags(ctx, tx, m, snap, &inserted, &skipped, true); err != nil {
		return inserted, skipped, m, err
	}
	if err := copyPolymorphicClicks(ctx, tx, m, snap, &inserted, &skipped, true); err != nil {
		return inserted, skipped, m, err
	}

	return inserted, skipped, m, nil
}

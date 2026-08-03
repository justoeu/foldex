package backup

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ────────────────────────────────────────────────────────────────────────────
// Duplicate mode.

func restoreDuplicate(ctx context.Context, tx pgx.Tx, snap *Snapshot) (Counts, []string, idMapping, error) {
	var inserted Counts
	warnings := []string{}
	m := newIDMapping()

	for _, t := range snap.Tags {
		name, err := uniqueTagName(ctx, tx, t.Name)
		if err != nil {
			return inserted, warnings, m, err
		}
		var newID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO tag (name, color, icon, created_at) VALUES ($1,$2,$3,$4) RETURNING id`,
			name, t.Color, t.Icon, t.CreatedAt).Scan(&newID); err != nil {
			return inserted, warnings, m, fmt.Errorf("insert tag %q: %w", name, err)
		}
		m.tagMap[t.ID] = newID
		inserted.Tags++
		if name != t.Name {
			warnings = append(warnings, fmt.Sprintf("tag %q renomeada para %q", t.Name, name))
		}
	}

	for _, f := range topoSortFolders(snap.Folders) {
		if _, err := insertFolderMapped(ctx, tx, &m, f); err != nil {
			return inserted, warnings, m, err
		}
		inserted.Folders++
	}

	for _, l := range snap.Links {
		folderID := mapOptionalID(m.folderMap, l.FolderID)
		slug, err := uniqueLinkSlug(ctx, tx, l.Slug, l.Title)
		if err != nil {
			return inserted, warnings, m, err
		}
		var newID int64
		err = tx.QueryRow(ctx, `
            INSERT INTO link (url, title, slug, description, favicon_url, og_image_url,
                              pinned, preview_status, preview_error, folder_id,
                              created_at, updated_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
            ON CONFLICT (url) DO NOTHING
            RETURNING id`,
			l.URL, l.Title, slug, l.Description, l.FaviconURL, l.OGImageURL,
			l.Pinned, l.PreviewStatus, l.PreviewError, folderID, l.CreatedAt, l.UpdatedAt).Scan(&newID)
		if errors.Is(err, pgx.ErrNoRows) {
			// URL UNIQUE — attach tags/clicks to existing row.
			warnings = append(warnings, fmt.Sprintf("link %q já existia — não duplicado (URL é UNIQUE)", l.URL))
			if err2 := tx.QueryRow(ctx, `SELECT id FROM link WHERE url=$1`, l.URL).Scan(&newID); err2 != nil {
				return inserted, warnings, m, fmt.Errorf("fetch existing link: %w", err2)
			}
		} else if err != nil {
			return inserted, warnings, m, fmt.Errorf("insert link: %w", err)
		} else {
			inserted.Links++
		}
		m.linkMap[l.ID] = newID
	}

	for _, n := range snap.Notes {
		if _, err := insertNoteMapped(ctx, tx, &m, n); err != nil {
			return inserted, warnings, m, err
		}
		inserted.Notes++
	}

	if err := attachPolymorphicTags(ctx, tx, m, snap, &inserted, nil, false); err != nil {
		return inserted, warnings, m, err
	}
	if err := copyPolymorphicClicks(ctx, tx, m, snap, &inserted, nil, false); err != nil {
		return inserted, warnings, m, err
	}

	if err := restoreAppSettings(ctx, tx, snap, false); err != nil {
		return inserted, warnings, m, err
	}

	return inserted, warnings, m, nil
}

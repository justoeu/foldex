package backup

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/links"
)

// readSnapshot reads all 5 tables inside the given tx and returns a Snapshot.
// Caller is responsible for the transaction (and the isolation level).
func readSnapshot(ctx context.Context, tx pgx.Tx) (*Snapshot, error) {
	snap := &Snapshot{Version: DatabaseSnapshotVersion}

	if err := scanRows(ctx, tx, `SELECT id, name, color, icon, created_at FROM tag ORDER BY id`,
		func(rows pgx.Rows) error {
			var t TagRow
			if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Icon, &t.CreatedAt); err != nil {
				return err
			}
			snap.Tags = append(snap.Tags, t)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}

	if err := scanRows(ctx, tx, `SELECT id, name, color, parent_id, created_at FROM folder ORDER BY id`,
		func(rows pgx.Rows) error {
			var f FolderRow
			if err := rows.Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt); err != nil {
				return err
			}
			snap.Folders = append(snap.Folders, f)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("folders: %w", err)
	}

	if err := scanRows(ctx, tx, `
        SELECT id, url, title, slug, description, favicon_url, og_image_url, pinned,
               preview_status, preview_error, folder_id, created_at, updated_at
        FROM link ORDER BY id`,
		func(rows pgx.Rows) error {
			var l LinkRow
			if err := rows.Scan(&l.ID, &l.URL, &l.Title, &l.Slug, &l.Description, &l.FaviconURL,
				&l.OGImageURL, &l.Pinned, &l.PreviewStatus, &l.PreviewError, &l.FolderID,
				&l.CreatedAt, &l.UpdatedAt); err != nil {
				return err
			}
			snap.Links = append(snap.Links, l)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("links: %w", err)
	}

	if err := scanRows(ctx, tx, `SELECT link_id, tag_id FROM link_tag ORDER BY link_id, tag_id`,
		func(rows pgx.Rows) error {
			var lt LinkTagRow
			if err := rows.Scan(&lt.LinkID, &lt.TagID); err != nil {
				return err
			}
			snap.LinkTags = append(snap.LinkTags, lt)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("link_tags: %w", err)
	}

	if err := scanRows(ctx, tx, `SELECT link_id, clicked_at FROM click_log ORDER BY id`,
		func(rows pgx.Rows) error {
			var c ClickRow
			if err := rows.Scan(&c.LinkID, &c.ClickedAt); err != nil {
				return err
			}
			snap.ClickLogs = append(snap.ClickLogs, c)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("click_logs: %w", err)
	}

	return snap, nil
}

// countConflicts checks how many incoming rows would collide with existing
// UNIQUE constraints, without writing.
func countConflicts(ctx context.Context, pool *pgxpool.Pool, snap *Snapshot) (Conflicts, error) {
	var c Conflicts

	if len(snap.Links) > 0 {
		urls := make([]string, len(snap.Links))
		for i, l := range snap.Links {
			urls[i] = l.URL
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM link WHERE url = ANY($1::text[])`, urls).Scan(&c.Links); err != nil {
			return c, fmt.Errorf("conflict links: %w", err)
		}
	}
	if len(snap.Tags) > 0 {
		names := make([]string, len(snap.Tags))
		for i, t := range snap.Tags {
			names[i] = t.Name
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM tag WHERE name = ANY($1::text[])`, names).Scan(&c.Tags); err != nil {
			return c, fmt.Errorf("conflict tags: %w", err)
		}
	}
	// folders have no unique constraint => 0 conflicts by construction.

	return c, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Wipe mode.

func wipeAll(ctx context.Context, tx pgx.Tx) (Counts, error) {
	var c Counts
	// Count what we're about to delete (for the report).
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM click_log`).Scan(&c.ClickLogs); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM link_tag`).Scan(&c.LinkTags); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM link`).Scan(&c.Links); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM folder`).Scan(&c.Folders); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tag`).Scan(&c.Tags); err != nil {
		return c, err
	}
	// TRUNCATE order respects FKs through CASCADE.
	if _, err := tx.Exec(ctx, `TRUNCATE TABLE click_log, link_tag, link, folder, tag RESTART IDENTITY CASCADE`); err != nil {
		return c, err
	}
	return c, nil
}

// restoreIdentity inserts everything from snap with the original IDs
// preserved. After all INSERTs, advances each sequence to max(id)+1 so future
// auto-IDs don't collide.
func restoreIdentity(ctx context.Context, tx pgx.Tx, snap *Snapshot) (idMapping, error) {
	m := newIDMapping()

	for _, t := range snap.Tags {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tag (id, name, color, icon, created_at) VALUES ($1,$2,$3,$4,$5)`,
			t.ID, t.Name, t.Color, t.Icon, t.CreatedAt); err != nil {
			return m, fmt.Errorf("insert tag %d: %w", t.ID, err)
		}
		m.tagMap[t.ID] = t.ID
	}
	// folders must be topologically sorted: parent_id is itself a foreign key
	// inside the same table.
	for _, f := range topoSortFolders(snap.Folders) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO folder (id, name, color, parent_id, created_at) VALUES ($1,$2,$3,$4,$5)`,
			f.ID, f.Name, f.Color, f.ParentID, f.CreatedAt); err != nil {
			return m, fmt.Errorf("insert folder %d: %w", f.ID, err)
		}
		m.folderMap[f.ID] = f.ID
	}
	for _, l := range snap.Links {
		// Wipe-mode preserves original IDs; slug comes straight from the
		// snapshot. If a backup somehow lacks slug (older format), derive
		// from title with a fallback to the id pattern that matches
		// migration 000009's backfill convention.
		slug := l.Slug
		if slug == "" {
			slug = links.Slugify(l.Title)
			if slug == "" {
				slug = fmt.Sprintf("link-%d", l.ID)
			}
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO link (id, url, title, slug, description, favicon_url, og_image_url,
                              pinned, preview_status, preview_error, folder_id,
                              created_at, updated_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			l.ID, l.URL, l.Title, slug, l.Description, l.FaviconURL, l.OGImageURL,
			l.Pinned, l.PreviewStatus, l.PreviewError, l.FolderID, l.CreatedAt, l.UpdatedAt); err != nil {
			return m, fmt.Errorf("insert link %d: %w", l.ID, err)
		}
		m.linkMap[l.ID] = l.ID
	}
	for _, lt := range snap.LinkTags {
		if _, err := tx.Exec(ctx,
			`INSERT INTO link_tag (link_id, tag_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			lt.LinkID, lt.TagID); err != nil {
			return m, fmt.Errorf("insert link_tag %d/%d: %w", lt.LinkID, lt.TagID, err)
		}
	}
	for _, c := range snap.ClickLogs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO click_log (link_id, clicked_at) VALUES ($1,$2)`,
			c.LinkID, c.ClickedAt); err != nil {
			return m, fmt.Errorf("insert click_log: %w", err)
		}
	}

	// Bump sequences past the largest restored id.
	for _, t := range []string{"tag", "folder", "link", "click_log"} {
		if _, err := tx.Exec(ctx,
			fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id)+1 FROM %s), 1), false)`, t, t)); err != nil {
			return m, fmt.Errorf("setval %s: %w", t, err)
		}
	}
	return m, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Skip mode.

func restoreSkip(ctx context.Context, tx pgx.Tx, snap *Snapshot) (Counts, Counts, idMapping, error) {
	var inserted, skipped Counts
	m := newIDMapping()

	for _, t := range snap.Tags {
		var newID int64
		err := tx.QueryRow(ctx, `
            INSERT INTO tag (name, color, icon, created_at)
            VALUES ($1,$2,$3,$4)
            ON CONFLICT (name) DO NOTHING
            RETURNING id`,
			t.Name, t.Color, t.Icon, t.CreatedAt).Scan(&newID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Already exists — fetch the existing id for the mapping.
			if err2 := tx.QueryRow(ctx, `SELECT id FROM tag WHERE name=$1`, t.Name).Scan(&newID); err2 != nil {
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
		// folder has no unique constraint → always insert new row, but remap
		// parent_id via the mapping.
		var parentID *int64
		if f.ParentID != nil {
			if mapped, ok := m.folderMap[*f.ParentID]; ok {
				parentID = &mapped
			}
		}
		var newID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO folder (name, color, parent_id, created_at) VALUES ($1,$2,$3,$4) RETURNING id`,
			f.Name, f.Color, parentID, f.CreatedAt).Scan(&newID); err != nil {
			return inserted, skipped, m, fmt.Errorf("insert folder: %w", err)
		}
		m.folderMap[f.ID] = newID
		inserted.Folders++
	}

	for _, l := range snap.Links {
		var folderID *int64
		if l.FolderID != nil {
			if mapped, ok := m.folderMap[*l.FolderID]; ok {
				folderID = &mapped
			}
		}
		// Slug: try to keep the original; if it collides with an existing
		// row (skip mode is conservative — preserves what's there), derive
		// a fresh suffix via uniqueLinkSlug. URL collision still wins via
		// ON CONFLICT (url).
		slug, err := uniqueLinkSlug(ctx, tx, l.Slug, l.Title)
		if err != nil {
			return inserted, skipped, m, err
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
			if err2 := tx.QueryRow(ctx, `SELECT id FROM link WHERE url=$1`, l.URL).Scan(&newID); err2 != nil {
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

	for _, lt := range snap.LinkTags {
		linkID, lok := m.linkMap[lt.LinkID]
		tagID, tok := m.tagMap[lt.TagID]
		if !lok || !tok {
			skipped.LinkTags++
			continue
		}
		ct, err := tx.Exec(ctx,
			`INSERT INTO link_tag (link_id, tag_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			linkID, tagID)
		if err != nil {
			return inserted, skipped, m, fmt.Errorf("insert link_tag: %w", err)
		}
		if ct.RowsAffected() == 0 {
			skipped.LinkTags++
		} else {
			inserted.LinkTags++
		}
	}

	for _, c := range snap.ClickLogs {
		linkID, ok := m.linkMap[c.LinkID]
		if !ok {
			skipped.ClickLogs++
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO click_log (link_id, clicked_at) VALUES ($1,$2)`, linkID, c.ClickedAt); err != nil {
			return inserted, skipped, m, fmt.Errorf("insert click_log: %w", err)
		}
		inserted.ClickLogs++
	}

	return inserted, skipped, m, nil
}

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
		var parentID *int64
		if f.ParentID != nil {
			if mapped, ok := m.folderMap[*f.ParentID]; ok {
				parentID = &mapped
			}
		}
		var newID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO folder (name, color, parent_id, created_at) VALUES ($1,$2,$3,$4) RETURNING id`,
			f.Name, f.Color, parentID, f.CreatedAt).Scan(&newID); err != nil {
			return inserted, warnings, m, fmt.Errorf("insert folder: %w", err)
		}
		m.folderMap[f.ID] = newID
		inserted.Folders++
	}

	for _, l := range snap.Links {
		var folderID *int64
		if l.FolderID != nil {
			if mapped, ok := m.folderMap[*l.FolderID]; ok {
				folderID = &mapped
			}
		}
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
			// URL conflict — can't honestly duplicate without breaking the
			// UNIQUE constraint. Fall back to using the existing row's id so
			// link_tags / click_logs still attach somewhere sensible.
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

	for _, lt := range snap.LinkTags {
		linkID, lok := m.linkMap[lt.LinkID]
		tagID, tok := m.tagMap[lt.TagID]
		if !lok || !tok {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO link_tag (link_id, tag_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			linkID, tagID); err != nil {
			return inserted, warnings, m, fmt.Errorf("insert link_tag: %w", err)
		}
		inserted.LinkTags++
	}

	for _, c := range snap.ClickLogs {
		linkID, ok := m.linkMap[c.LinkID]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO click_log (link_id, clicked_at) VALUES ($1,$2)`, linkID, c.ClickedAt); err != nil {
			return inserted, warnings, m, fmt.Errorf("insert click_log: %w", err)
		}
		inserted.ClickLogs++
	}

	return inserted, warnings, m, nil
}

// uniqueLinkSlug returns the original slug if free, else the original with
// `-2`, `-3`, … suffix. Falls back to slugify(title) when the snapshot has
// an empty/missing slug (older backups predating migration 000009 — those
// don't exist in practice, but defensive). Caps at 1000 attempts.
func uniqueLinkSlug(ctx context.Context, tx pgx.Tx, slug, title string) (string, error) {
	base := slug
	if base == "" {
		base = links.Slugify(title)
		if base == "" {
			base = "link-restored"
		}
	}
	candidate := base
	for attempt := 1; attempt < 1000; attempt++ {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM link WHERE slug = $1)`, candidate).Scan(&exists); err != nil {
			return "", fmt.Errorf("check slug availability: %w", err)
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, attempt+1)
	}
	return "", fmt.Errorf("uniqueLinkSlug: exhausted attempts for %q", base)
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

// ────────────────────────────────────────────────────────────────────────────
// ID mapping (old → new).

type idMapping struct {
	tagMap    map[int64]int64
	folderMap map[int64]int64
	linkMap   map[int64]int64
}

func newIDMapping() idMapping {
	return idMapping{
		tagMap:    make(map[int64]int64),
		folderMap: make(map[int64]int64),
		linkMap:   make(map[int64]int64),
	}
}

// remapFileKey translates `screenshots/123.png` → `screenshots/456.png` when
// link 123 was remapped to 456 by ModeDuplicate. Returns (newKey, true) if a
// mapping applies, (key, false) otherwise.
func (m idMapping) remapFileKey(key string) (string, bool) {
	var prefix string
	switch {
	case startsWith(key, "screenshots/"):
		prefix = "screenshots/"
	case startsWith(key, "images/"):
		prefix = "images/"
	default:
		return key, false
	}
	rest := key[len(prefix):]
	dot := indexByte(rest, '.')
	if dot <= 0 {
		return key, false
	}
	var oldID int64
	if _, err := fmt.Sscan(rest[:dot], &oldID); err != nil {
		return key, false
	}
	newID, ok := m.linkMap[oldID]
	if !ok || newID == oldID {
		return key, false
	}
	return prefix + fmt.Sprintf("%d", newID) + rest[dot:], true
}

// ────────────────────────────────────────────────────────────────────────────
// Topological sort of folders by parent_id so we can INSERT roots first.

func topoSortFolders(in []FolderRow) []FolderRow {
	// Stable topological pass: any folder whose parent has already been
	// emitted is safe to emit next.
	seen := make(map[int64]bool, len(in))
	out := make([]FolderRow, 0, len(in))
	remaining := append([]FolderRow{}, in...)

	for len(remaining) > 0 {
		progress := false
		// Fresh slice each pass — sharing remaining's backing array would let
		// `append(next, ...)` overwrite slots the range loop has not visited yet,
		// silently dropping or duplicating folders.
		next := make([]FolderRow, 0, len(remaining))
		for _, f := range remaining {
			if f.ParentID == nil || seen[*f.ParentID] {
				out = append(out, f)
				seen[f.ID] = true
				progress = true
				continue
			}
			next = append(next, f)
		}
		remaining = next
		if !progress {
			// Cycle or dangling parent — emit the rest as-is so we don't loop.
			out = append(out, remaining...)
			break
		}
	}
	return out
}

// ────────────────────────────────────────────────────────────────────────────
// scanRows is a tiny helper around pgx.Rows that calls back per row.

func scanRows(ctx context.Context, tx pgx.Tx, sql string, fn func(pgx.Rows) error) error {
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Mini-helpers to avoid pulling in `strings` for two operations.
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

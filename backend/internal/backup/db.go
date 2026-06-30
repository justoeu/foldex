package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/links"
	"foldex/internal/pkg/htmlsanitize"
)

// sanitizeNoteBody re-derives a note's body_html/body_text the same way
// notes.CreateInput.Normalize() does. The backup zip is a trust boundary —
// database.json's note rows are attacker-controlled the same way tag/folder
// colors are (Snapshot.Sanitize's rationale) — and restore writes go straight
// to SQL via CopyFrom/INSERT, bypassing notes.Repository (and therefore
// notes.CreateInput.Normalize) entirely. Without this, a crafted backup zip
// could plant <script>/onerror=/javascript: payloads that later render as raw
// template.HTML on the public, unauthenticated GET /n/{id-or-slug} route.
func sanitizeNoteBody(bodyHTML string) (string, string) {
	clean := htmlsanitize.Sanitize(bodyHTML)
	return clean, htmlsanitize.PlainText(clean)
}

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

	if err := scanRows(ctx, tx, `
        SELECT id, title, slug, body_html, body_text, pinned, folder_id, cover_url, created_at, updated_at
        FROM note ORDER BY id`,
		func(rows pgx.Rows) error {
			var n NoteRow
			if err := rows.Scan(&n.ID, &n.Title, &n.Slug, &n.BodyHTML, &n.BodyText, &n.Pinned,
				&n.FolderID, &n.CoverURL, &n.CreatedAt, &n.UpdatedAt); err != nil {
				return err
			}
			snap.Notes = append(snap.Notes, n)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("notes: %w", err)
	}

	// link_tag/click_log are polymorphized (migration 000014) — split the read
	// by entity_kind so the JSON wire shape stays one array per entity kind.
	if err := scanRows(ctx, tx, `SELECT entity_id, tag_id FROM link_tag WHERE entity_kind = 'link' ORDER BY entity_id, tag_id`,
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

	if err := scanRows(ctx, tx, `SELECT entity_id, tag_id FROM link_tag WHERE entity_kind = 'note' ORDER BY entity_id, tag_id`,
		func(rows pgx.Rows) error {
			var nt NoteTagRow
			if err := rows.Scan(&nt.NoteID, &nt.TagID); err != nil {
				return err
			}
			snap.NoteTags = append(snap.NoteTags, nt)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("note_tags: %w", err)
	}

	if err := scanRows(ctx, tx, `SELECT entity_id, clicked_at FROM click_log WHERE entity_kind = 'link' ORDER BY id`,
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

	if err := scanRows(ctx, tx, `SELECT entity_id, clicked_at FROM click_log WHERE entity_kind = 'note' ORDER BY id`,
		func(rows pgx.Rows) error {
			var c NoteClickRow
			if err := rows.Scan(&c.NoteID, &c.ClickedAt); err != nil {
				return err
			}
			snap.NoteClicks = append(snap.NoteClicks, c)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("note_clicks: %w", err)
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
	// Count what we're about to delete (for the report). click_log/link_tag
	// are polymorphic — these counts span both link and note rows, matching
	// the combined LinkTags/ClickLogs fields restoreIdentity reports back.
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM click_log`).Scan(&c.ClickLogs); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM link_tag`).Scan(&c.LinkTags); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM link`).Scan(&c.Links); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM note`).Scan(&c.Notes); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM folder`).Scan(&c.Folders); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tag`).Scan(&c.Tags); err != nil {
		return c, err
	}
	// TRUNCATE order respects FKs through CASCADE. `note` has no FK CASCADE
	// dependents of its own (link_tag/click_log lost their FK to link/note in
	// migration 000014 — cascade is app-level elsewhere, but a blanket TRUNCATE
	// here doesn't need it since every listed table is wiped together).
	if _, err := tx.Exec(ctx, `TRUNCATE TABLE click_log, link_tag, note, link, folder, tag RESTART IDENTITY CASCADE`); err != nil {
		return c, err
	}
	return c, nil
}

// restoreIdentity inserts everything from snap with the original IDs
// preserved. After all INSERTs, advances each sequence to max(id)+1 so future
// auto-IDs don't collide.
//
// All five loops use pgx.CopyFrom (PostgreSQL COPY protocol) instead of
// per-row INSERTs. The wipe path handles the worst-case restore volume
// (a power-user backup of hundreds of thousands of click_logs); per-row
// INSERTs amortized to one network round-trip per row turned a 1M-row
// click_log restore into 1M sequential INSERTs. CopyFrom batches them in a
// single streaming upload — typically 10-50× fewer round-trips. CopyFrom
// is safe here because wipe mode already TRUNCATEd, so there are no
// conflicts to handle and no RETURNING values to capture.
func restoreIdentity(ctx context.Context, tx pgx.Tx, snap *Snapshot) (idMapping, error) {
	m := newIDMapping()

	if len(snap.Tags) > 0 {
		rows := make([][]any, 0, len(snap.Tags))
		for _, t := range snap.Tags {
			rows = append(rows, []any{t.ID, t.Name, t.Color, t.Icon, t.CreatedAt})
			m.tagMap[t.ID] = t.ID
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"tag"},
			[]string{"id", "name", "color", "icon", "created_at"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return m, fmt.Errorf("copy tag: %w", err)
		}
	}

	// folders must be topologically sorted: parent_id is itself a foreign key
	// inside the same table. CopyFrom preserves slice order.
	if len(snap.Folders) > 0 {
		rows := make([][]any, 0, len(snap.Folders))
		for _, f := range topoSortFolders(snap.Folders) {
			rows = append(rows, []any{f.ID, f.Name, f.Color, f.ParentID, f.CreatedAt})
			m.folderMap[f.ID] = f.ID
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"folder"},
			[]string{"id", "name", "color", "parent_id", "created_at"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return m, fmt.Errorf("copy folder: %w", err)
		}
	}

	if len(snap.Links) > 0 {
		rows := make([][]any, 0, len(snap.Links))
		for _, l := range snap.Links {
			// Slug fallback for older backups predating migration 000009:
			// derive from title, fall back to the id pattern matching the
			// migration's backfill convention. Computed up-front since
			// CopyFrom can't run a RETURNING clause.
			slug := l.Slug
			if slug == "" {
				slug = links.Slugify(l.Title)
				if slug == "" {
					slug = fmt.Sprintf("link-%d", l.ID)
				}
			}
			rows = append(rows, []any{
				l.ID, l.URL, l.Title, slug, l.Description, l.FaviconURL, l.OGImageURL,
				l.Pinned, l.PreviewStatus, l.PreviewError, l.FolderID, l.CreatedAt, l.UpdatedAt,
			})
			m.linkMap[l.ID] = l.ID
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"link"},
			[]string{"id", "url", "title", "slug", "description", "favicon_url", "og_image_url",
				"pinned", "preview_status", "preview_error", "folder_id", "created_at", "updated_at"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return m, fmt.Errorf("copy link: %w", err)
		}
	}

	if len(snap.Notes) > 0 {
		rows := make([][]any, 0, len(snap.Notes))
		for _, n := range snap.Notes {
			// Same slug fallback as links, for older/hand-edited snapshots.
			slug := n.Slug
			if slug == "" {
				slug = links.Slugify(n.Title)
				if slug == "" {
					slug = fmt.Sprintf("note-%d", n.ID)
				}
			}
			bodyHTML, bodyText := sanitizeNoteBody(n.BodyHTML)
			rows = append(rows, []any{
				n.ID, n.Title, slug, bodyHTML, bodyText, n.Pinned, n.FolderID, n.CoverURL,
				n.CreatedAt, n.UpdatedAt,
			})
			m.noteMap[n.ID] = n.ID
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"note"},
			[]string{"id", "title", "slug", "body_html", "body_text", "pinned", "folder_id", "cover_url",
				"created_at", "updated_at"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return m, fmt.Errorf("copy note: %w", err)
		}
	}

	// link_tag/click_log are polymorphic — combine the link-kind and note-kind
	// rows from the snapshot into one CopyFrom batch per table, each row
	// carrying its own entity_kind.
	if len(snap.LinkTags)+len(snap.NoteTags) > 0 {
		rows := make([][]any, 0, len(snap.LinkTags)+len(snap.NoteTags))
		for _, lt := range snap.LinkTags {
			rows = append(rows, []any{"link", lt.LinkID, lt.TagID})
		}
		for _, nt := range snap.NoteTags {
			rows = append(rows, []any{"note", nt.NoteID, nt.TagID})
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"link_tag"},
			[]string{"entity_kind", "entity_id", "tag_id"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return m, fmt.Errorf("copy link_tag: %w", err)
		}
	}

	if len(snap.ClickLogs)+len(snap.NoteClicks) > 0 {
		rows := make([][]any, 0, len(snap.ClickLogs)+len(snap.NoteClicks))
		for _, c := range snap.ClickLogs {
			rows = append(rows, []any{"link", c.LinkID, c.ClickedAt})
		}
		for _, c := range snap.NoteClicks {
			rows = append(rows, []any{"note", c.NoteID, c.ClickedAt})
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"click_log"},
			[]string{"entity_kind", "entity_id", "clicked_at"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return m, fmt.Errorf("copy click_log: %w", err)
		}
	}

	// Bump sequences past the largest restored id.
	for _, t := range []string{"tag", "folder", "link", "note", "click_log"} {
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

	// Notes have no natural content-identity key the way links have URL — two
	// distinct notes can legitimately share a title. Skip mode therefore
	// always inserts a fresh row (slug uniquified the same way links handle
	// slug collisions); there is no "this note already exists, leave it
	// alone" detection for notes in v1.
	for _, n := range snap.Notes {
		var folderID *int64
		if n.FolderID != nil {
			if mapped, ok := m.folderMap[*n.FolderID]; ok {
				folderID = &mapped
			}
		}
		slug, err := uniqueNoteSlug(ctx, tx, n.Slug, n.Title)
		if err != nil {
			return inserted, skipped, m, err
		}
		bodyHTML, bodyText := sanitizeNoteBody(n.BodyHTML)
		var newID int64
		if err := tx.QueryRow(ctx, `
            INSERT INTO note (title, slug, body_html, body_text, pinned, folder_id, cover_url, created_at, updated_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
            RETURNING id`,
			n.Title, slug, bodyHTML, bodyText, n.Pinned, folderID, n.CoverURL, n.CreatedAt, n.UpdatedAt).Scan(&newID); err != nil {
			return inserted, skipped, m, fmt.Errorf("insert note: %w", err)
		}
		m.noteMap[n.ID] = newID
		inserted.Notes++
	}

	for _, lt := range snap.LinkTags {
		linkID, lok := m.linkMap[lt.LinkID]
		tagID, tok := m.tagMap[lt.TagID]
		if !lok || !tok {
			skipped.LinkTags++
			continue
		}
		ct, err := tx.Exec(ctx,
			`INSERT INTO link_tag (entity_kind, entity_id, tag_id) VALUES ('link', $1,$2) ON CONFLICT DO NOTHING`,
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

	for _, nt := range snap.NoteTags {
		noteID, nok := m.noteMap[nt.NoteID]
		tagID, tok := m.tagMap[nt.TagID]
		if !nok || !tok {
			skipped.LinkTags++
			continue
		}
		ct, err := tx.Exec(ctx,
			`INSERT INTO link_tag (entity_kind, entity_id, tag_id) VALUES ('note', $1,$2) ON CONFLICT DO NOTHING`,
			noteID, tagID)
		if err != nil {
			return inserted, skipped, m, fmt.Errorf("insert note tag: %w", err)
		}
		if ct.RowsAffected() == 0 {
			skipped.LinkTags++
		} else {
			inserted.LinkTags++
		}
	}

	// click_log is the highest-volume table in a power-user backup (often
	// 100k+ rows). Per-row INSERT turned this loop into the dominant restore
	// cost; CopyFrom streams it in a single COPY. The id-mapping filter
	// (linkID must exist in the restored set) is applied up-front while
	// building rows; unmapped clicks are counted as skipped. Note clicks are
	// folded into the same CopyFrom batch (combined ClickLogs counters).
	if len(snap.ClickLogs)+len(snap.NoteClicks) > 0 {
		rows := make([][]any, 0, len(snap.ClickLogs)+len(snap.NoteClicks))
		for _, c := range snap.ClickLogs {
			linkID, ok := m.linkMap[c.LinkID]
			if !ok {
				skipped.ClickLogs++
				continue
			}
			rows = append(rows, []any{"link", linkID, c.ClickedAt})
		}
		for _, c := range snap.NoteClicks {
			noteID, ok := m.noteMap[c.NoteID]
			if !ok {
				skipped.ClickLogs++
				continue
			}
			rows = append(rows, []any{"note", noteID, c.ClickedAt})
		}
		if len(rows) > 0 {
			if _, err := tx.CopyFrom(ctx,
				pgx.Identifier{"click_log"},
				[]string{"entity_kind", "entity_id", "clicked_at"},
				pgx.CopyFromRows(rows),
			); err != nil {
				return inserted, skipped, m, fmt.Errorf("copy click_log: %w", err)
			}
		}
		inserted.ClickLogs += int64(len(rows))
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

	// Notes always duplicate cleanly (no UNIQUE-url-style identity collision
	// like links can hit) — every note gets a brand new row with a
	// collision-resolved slug.
	for _, n := range snap.Notes {
		var folderID *int64
		if n.FolderID != nil {
			if mapped, ok := m.folderMap[*n.FolderID]; ok {
				folderID = &mapped
			}
		}
		slug, err := uniqueNoteSlug(ctx, tx, n.Slug, n.Title)
		if err != nil {
			return inserted, warnings, m, err
		}
		bodyHTML, bodyText := sanitizeNoteBody(n.BodyHTML)
		var newID int64
		if err := tx.QueryRow(ctx, `
            INSERT INTO note (title, slug, body_html, body_text, pinned, folder_id, cover_url, created_at, updated_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
            RETURNING id`,
			n.Title, slug, bodyHTML, bodyText, n.Pinned, folderID, n.CoverURL, n.CreatedAt, n.UpdatedAt).Scan(&newID); err != nil {
			return inserted, warnings, m, fmt.Errorf("insert note: %w", err)
		}
		m.noteMap[n.ID] = newID
		inserted.Notes++
	}

	for _, lt := range snap.LinkTags {
		linkID, lok := m.linkMap[lt.LinkID]
		tagID, tok := m.tagMap[lt.TagID]
		if !lok || !tok {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO link_tag (entity_kind, entity_id, tag_id) VALUES ('link', $1,$2) ON CONFLICT DO NOTHING`,
			linkID, tagID); err != nil {
			return inserted, warnings, m, fmt.Errorf("insert link_tag: %w", err)
		}
		inserted.LinkTags++
	}

	for _, nt := range snap.NoteTags {
		noteID, nok := m.noteMap[nt.NoteID]
		tagID, tok := m.tagMap[nt.TagID]
		if !nok || !tok {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO link_tag (entity_kind, entity_id, tag_id) VALUES ('note', $1,$2) ON CONFLICT DO NOTHING`,
			noteID, tagID); err != nil {
			return inserted, warnings, m, fmt.Errorf("insert note tag: %w", err)
		}
		inserted.LinkTags++
	}

	// click_log: CopyFrom for the same reason as restoreSkip — high-volume,
	// no conflict handling needed. Pre-filter by mapping presence.
	if len(snap.ClickLogs)+len(snap.NoteClicks) > 0 {
		rows := make([][]any, 0, len(snap.ClickLogs)+len(snap.NoteClicks))
		for _, c := range snap.ClickLogs {
			linkID, ok := m.linkMap[c.LinkID]
			if !ok {
				continue
			}
			rows = append(rows, []any{"link", linkID, c.ClickedAt})
		}
		for _, c := range snap.NoteClicks {
			noteID, ok := m.noteMap[c.NoteID]
			if !ok {
				continue
			}
			rows = append(rows, []any{"note", noteID, c.ClickedAt})
		}
		if len(rows) > 0 {
			if _, err := tx.CopyFrom(ctx,
				pgx.Identifier{"click_log"},
				[]string{"entity_kind", "entity_id", "clicked_at"},
				pgx.CopyFromRows(rows),
			); err != nil {
				return inserted, warnings, m, fmt.Errorf("copy click_log: %w", err)
			}
		}
		inserted.ClickLogs += int64(len(rows))
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

// uniqueNoteSlug is the note-table sibling of uniqueLinkSlug — same
// fallback/collision-suffix strategy, against the `note` table instead of
// `link`.
func uniqueNoteSlug(ctx context.Context, tx pgx.Tx, slug, title string) (string, error) {
	base := slug
	if base == "" {
		base = links.Slugify(title)
		if base == "" {
			base = "note-restored"
		}
	}
	candidate := base
	for attempt := 1; attempt < 1000; attempt++ {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM note WHERE slug = $1)`, candidate).Scan(&exists); err != nil {
			return "", fmt.Errorf("check note slug availability: %w", err)
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, attempt+1)
	}
	return "", fmt.Errorf("uniqueNoteSlug: exhausted attempts for %q", base)
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
	noteMap   map[int64]int64
}

func newIDMapping() idMapping {
	return idMapping{
		tagMap:    make(map[int64]int64),
		folderMap: make(map[int64]int64),
		linkMap:   make(map[int64]int64),
		noteMap:   make(map[int64]int64),
	}
}

// remapFileKey translates `screenshots/123.png` → `screenshots/456.png` when
// link 123 was remapped to 456 by ModeDuplicate. Returns (newKey, true) if a
// mapping applies, (key, false) otherwise.
//
// Note inline images are NOT handled here: their object keys are UUID-named
// (`notes/<uuid>.jpg`, written by the note image-upload endpoint) rather than
// id-named, so the key never encodes a note id that ModeDuplicate could remap
// — the same UUID-keyed object is valid for both the original and the
// duplicated note row.
func (m idMapping) remapFileKey(key string) (string, bool) {
	var prefix string
	switch {
	case strings.HasPrefix(key, "screenshots/"):
		prefix = "screenshots/"
	case strings.HasPrefix(key, "images/"):
		prefix = "images/"
	default:
		return key, false
	}
	rest := key[len(prefix):]
	dot := strings.IndexByte(rest, '.')
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

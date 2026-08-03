package backup

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

	if err := scanRows(ctx, tx, `SELECT id, name, color, parent_id, password_hash, password_hint, created_at FROM folder ORDER BY id`,
		func(rows pgx.Rows) error {
			var f FolderRow
			if err := rows.Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.PasswordHash, &f.PasswordHint, &f.CreatedAt); err != nil {
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

	if err := scanRows(ctx, tx, `SELECT key, value, updated_at FROM app_setting ORDER BY key`,
		func(rows pgx.Rows) error {
			var s AppSettingRow
			if err := rows.Scan(&s.Key, &s.Value, &s.UpdatedAt); err != nil {
				return err
			}
			snap.AppSettings = append(snap.AppSettings, s)
			return nil
		},
	); err != nil {
		return nil, fmt.Errorf("app_settings: %w", err)
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

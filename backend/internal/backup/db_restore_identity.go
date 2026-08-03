package backup

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/notes"
	slugpkg "foldex/internal/pkg/slug"
)

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
			rows = append(rows, []any{f.ID, f.Name, f.Color, f.ParentID, f.PasswordHash, f.PasswordHint, f.CreatedAt})
			m.folderMap[f.ID] = f.ID
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"folder"},
			[]string{"id", "name", "color", "parent_id", "password_hash", "password_hint", "created_at"},
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
				slug = slugpkg.Slugify(l.Title)
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
				slug = slugpkg.Slugify(n.Title)
				if slug == "" {
					slug = fmt.Sprintf("note-%d", n.ID)
				}
			}
			bodyHTML, bodyText := notes.SanitizeBody(n.BodyHTML)
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
	// app_setting was TRUNCATEd by wipeAll, so overwrite semantics are moot —
	// but keep them explicit so the snapshot's settings always win in wipe mode.
	if err := restoreAppSettings(ctx, tx, snap, true); err != nil {
		return m, err
	}
	return m, nil
}

// restoreAppSettings writes the snapshot's app_setting KV rows verbatim (never
// re-hashing the master password value). overwrite=true (wipe mode) lets the
// snapshot's value win on key conflict; overwrite=false (skip/duplicate) leaves
// any existing setting untouched — a singleton setting can't be "duplicated".
func restoreAppSettings(ctx context.Context, tx pgx.Tx, snap *Snapshot, overwrite bool) error {
	conflict := `ON CONFLICT (key) DO NOTHING`
	if overwrite {
		conflict = `ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`
	}
	for _, s := range snap.AppSettings {
		if _, err := tx.Exec(ctx,
			`INSERT INTO app_setting (key, value, updated_at) VALUES ($1,$2,$3) `+conflict,
			s.Key, s.Value, s.UpdatedAt); err != nil {
			return fmt.Errorf("restore app_setting %q: %w", s.Key, err)
		}
	}
	return nil
}

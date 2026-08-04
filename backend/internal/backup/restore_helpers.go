package backup

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/notes"

	"foldex/internal/pkg/authctx"
)

// mapOptionalID remaps *old through m when present.
func mapOptionalID(m map[int64]int64, old *int64) *int64 {
	if old == nil {
		return nil
	}
	if mapped, ok := m[*old]; ok {
		return &mapped
	}
	return nil
}

// insertFolderMapped inserts one folder with parent remapped via m.folderMap.
func insertFolderMapped(ctx context.Context, tx pgx.Tx, uid authctx.UserID, m *idMapping, f FolderRow) (int64, error) {
	parentID := mapOptionalID(m.folderMap, f.ParentID)
	var newID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO folder (user_id, name, color, parent_id, password_hash, password_hint, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		int64(uid), f.Name, f.Color, parentID, f.PasswordHash, f.PasswordHint, f.CreatedAt).Scan(&newID); err != nil {
		return 0, fmt.Errorf("insert folder: %w", err)
	}
	m.folderMap[f.ID] = newID
	return newID, nil
}

// insertNoteMapped inserts one note with folder remapped and body sanitized.
func insertNoteMapped(ctx context.Context, tx pgx.Tx, uid authctx.UserID, m *idMapping, n NoteRow) (int64, error) {
	folderID := mapOptionalID(m.folderMap, n.FolderID)
	slug, err := uniqueNoteSlug(ctx, tx, n.Slug, n.Title)
	if err != nil {
		return 0, err
	}
	bodyHTML, bodyText := notes.SanitizeBody(n.BodyHTML)
	var newID int64
	if err := tx.QueryRow(ctx, `
            INSERT INTO note (user_id, title, slug, body_html, body_text, pinned, folder_id, cover_url, created_at, updated_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
            RETURNING id`,
		int64(uid), n.Title, slug, bodyHTML, bodyText, n.Pinned, folderID, n.CoverURL, n.CreatedAt, n.UpdatedAt).Scan(&newID); err != nil {
		return 0, fmt.Errorf("insert note: %w", err)
	}
	m.noteMap[n.ID] = newID
	return newID, nil
}

// attachPolymorphicTags inserts link_tag rows for links and notes using the
// id mapping. Batches via temp table + CopyFrom + INSERT…ON CONFLICT
// (N1-NEX-009) instead of per-row Exec. When countSkips is true, unmapped or
// conflict-no-op rows bump skipped.LinkTags.
func attachPolymorphicTags(ctx context.Context, tx pgx.Tx, m idMapping, snap *Snapshot, inserted, skipped *Counts, countSkips bool) error {
	rows := make([][]any, 0, len(snap.LinkTags)+len(snap.NoteTags))
	var unmapped int64
	for _, lt := range snap.LinkTags {
		linkID, lok := m.linkMap[lt.LinkID]
		tagID, tok := m.tagMap[lt.TagID]
		if !lok || !tok {
			unmapped++
			continue
		}
		rows = append(rows, []any{"link", linkID, tagID})
	}
	for _, nt := range snap.NoteTags {
		noteID, nok := m.noteMap[nt.NoteID]
		tagID, tok := m.tagMap[nt.TagID]
		if !nok || !tok {
			unmapped++
			continue
		}
		rows = append(rows, []any{"note", noteID, tagID})
	}
	if countSkips && skipped != nil && unmapped > 0 {
		skipped.LinkTags += unmapped
	}
	if len(rows) == 0 {
		return nil
	}

	if _, err := tx.Exec(ctx, `
        CREATE TEMP TABLE _restore_link_tag (
            entity_kind text NOT NULL,
            entity_id   bigint NOT NULL,
            tag_id      bigint NOT NULL
        ) ON COMMIT DROP
    `); err != nil {
		return fmt.Errorf("create restore link_tag temp: %w", err)
	}
	if _, err := tx.CopyFrom(ctx,
		pgx.Identifier{"_restore_link_tag"},
		[]string{"entity_kind", "entity_id", "tag_id"},
		pgx.CopyFromRows(rows),
	); err != nil {
		return fmt.Errorf("copy restore link_tag temp: %w", err)
	}
	ct, err := tx.Exec(ctx, `
        INSERT INTO link_tag (entity_kind, entity_id, tag_id)
        SELECT entity_kind, entity_id, tag_id FROM _restore_link_tag
        ON CONFLICT DO NOTHING
    `)
	if err != nil {
		return fmt.Errorf("insert link_tag batch: %w", err)
	}
	nIns := ct.RowsAffected()
	if inserted != nil {
		inserted.LinkTags += nIns
	}
	if countSkips && skipped != nil {
		// Conflicts / no-ops among the mapped batch.
		if n := int64(len(rows)) - nIns; n > 0 {
			skipped.LinkTags += n
		}
	}
	return nil
}

// copyPolymorphicClicks bulk-inserts click_log for mapped links and notes.
func copyPolymorphicClicks(ctx context.Context, tx pgx.Tx, uid authctx.UserID, m idMapping, snap *Snapshot, inserted, skipped *Counts, countSkips bool) error {
	if len(snap.ClickLogs)+len(snap.NoteClicks) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(snap.ClickLogs)+len(snap.NoteClicks))
	for _, c := range snap.ClickLogs {
		linkID, ok := m.linkMap[c.LinkID]
		if !ok {
			if countSkips && skipped != nil {
				skipped.ClickLogs++
			}
			continue
		}
		rows = append(rows, []any{"link", linkID, c.ClickedAt, int64(uid)})
	}
	for _, c := range snap.NoteClicks {
		noteID, ok := m.noteMap[c.NoteID]
		if !ok {
			if countSkips && skipped != nil {
				skipped.ClickLogs++
			}
			continue
		}
		rows = append(rows, []any{"note", noteID, c.ClickedAt, int64(uid)})
	}
	if len(rows) == 0 {
		return nil
	}
	if _, err := tx.CopyFrom(ctx,
		pgx.Identifier{"click_log"},
		[]string{"entity_kind", "entity_id", "clicked_at", "user_id"},
		pgx.CopyFromRows(rows),
	); err != nil {
		return fmt.Errorf("copy click_log: %w", err)
	}
	if inserted != nil {
		inserted.ClickLogs += int64(len(rows))
	}
	return nil
}

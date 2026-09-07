package backup

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"foldex/internal/imageopt"
	"foldex/internal/notemedia"
	"foldex/internal/notes"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/clicklog"
)

const maxRestoredNoteMediaBytes = 16 << 20

type preparedNoteMediaFile struct {
	offset      int64
	size        int64
	contentType string
}

type preparedNoteMediaRestore struct {
	mapping map[string]string
	files   map[string]preparedNoteMediaFile
	spool   *os.File
}

func (p *preparedNoteMediaRestore) cleanup() {
	if p == nil || p.spool == nil {
		return
	}
	name := p.spool.Name()
	_ = p.spool.Close()
	_ = os.Remove(name)
}

// noteMediaSpool owns the two independent restore caps and the spool-file
// bookkeeping behind them:
//
//   - per-file: one optimized image may not exceed maxRestoredNoteMediaBytes;
//   - aggregate: everything spooled so far may not exceed the archive's
//     expanded-bytes budget (maxArchiveExpandedBytes in production).
//
// They sit two lines apart in the write path but guard different invariants —
// the per-file one bounds a single decode, the aggregate one bounds the whole
// restore's disk footprint — so each is separately testable via budget.
type noteMediaSpool struct {
	file            *os.File
	size            int64
	aggregateBudget int64
}

func newNoteMediaSpool() *noteMediaSpool {
	return &noteMediaSpool{aggregateBudget: maxArchiveExpandedBytes}
}

// write stores one optimized image, lazily creating the spool file on first
// use, and returns its offset window.
func (s *noteMediaSpool) write(data []byte, contentType string) (preparedNoteMediaFile, error) {
	if int64(len(data)) > maxRestoredNoteMediaBytes || int64(len(data)) > s.aggregateBudget-s.size {
		return preparedNoteMediaFile{}, invalidBackup("optimized note media exceeds restore limits")
	}
	if s.file == nil {
		f, err := os.CreateTemp("", "foldex-backup-note-media-*.bin")
		if err != nil {
			return preparedNoteMediaFile{}, fmt.Errorf("create note media spool: %w", err)
		}
		s.file = f
	}
	offset := s.size
	n, err := s.file.Write(data)
	if err != nil {
		return preparedNoteMediaFile{}, fmt.Errorf("write note media spool: %w", err)
	}
	if n != len(data) {
		return preparedNoteMediaFile{}, io.ErrShortWrite
	}
	s.size += int64(n)
	return preparedNoteMediaFile{offset: offset, size: int64(n), contentType: contentType}, nil
}

func prepareNoteMediaRestore(ctx context.Context, snap *Snapshot, zr *zip.Reader) (_ *preparedNoteMediaRestore, err error) {
	prepared := &preparedNoteMediaRestore{
		mapping: make(map[string]string),
		files:   make(map[string]preparedNoteMediaFile),
	}
	defer func() {
		if err != nil {
			prepared.cleanup()
		}
	}()
	spool := newNoteMediaSpool()
	fileEntries := zipFileEntries(zr, "files/")
	for i := range snap.Notes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := spoolNoteMedia(ctx, &snap.Notes[i], fileEntries, prepared, spool); err != nil {
			return nil, err
		}
	}
	prepared.spool = spool.file
	return prepared, nil
}

// spoolNoteMedia sanitizes one note's body, spools every referenced media
// object that exists in the archive, and rewrites the note's references to
// the restored keys.
func spoolNoteMedia(ctx context.Context, note *NoteRow, fileEntries map[string]*zip.File, prepared *preparedNoteMediaRestore, spool *noteMediaSpool) error {
	bodyHTML, _ := notes.SanitizeBody(note.BodyHTML)
	note.BodyHTML = bodyHTML
	values := []string{bodyHTML}
	if note.CoverURL != nil {
		values = append(values, *note.CoverURL)
	}
	for _, oldKey := range notemedia.Keys(values...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, exists := prepared.mapping[oldKey]; exists {
			continue
		}
		entry, exists := fileEntries["files/"+oldKey]
		if !exists {
			continue
		}
		opt, err := optimizeRestoredNoteMedia(ctx, entry)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return invalidBackup("backup contains invalid note media")
		}
		file, err := spool.write(opt.Data, opt.ContentType)
		if err != nil {
			return err
		}
		prepared.files[oldKey] = file
		prepared.mapping[oldKey] = "notes/" + uuid.NewString() + "." + opt.Ext
	}
	note.BodyHTML = notemedia.Rewrite(note.BodyHTML, prepared.mapping)
	if note.CoverURL != nil {
		rewritten := notemedia.Rewrite(*note.CoverURL, prepared.mapping)
		note.CoverURL = &rewritten
	}
	return nil
}

func optimizeRestoredNoteMedia(ctx context.Context, entry *zip.File) (imageopt.Result, error) {
	if entry.UncompressedSize64 > maxRestoredNoteMediaBytes {
		return imageopt.Result{}, fmt.Errorf("note media exceeds restore limit")
	}
	r, err := entry.Open()
	if err != nil {
		return imageopt.Result{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: r}, maxRestoredNoteMediaBytes+1))
	closeErr := r.Close()
	if readErr != nil {
		return imageopt.Result{}, readErr
	}
	if closeErr != nil {
		return imageopt.Result{}, closeErr
	}
	if len(data) > maxRestoredNoteMediaBytes {
		return imageopt.Result{}, fmt.Errorf("note media exceeds restore limit")
	}
	optimized, err := imageopt.Optimize(data, imageopt.Options{MaxDim: 1024, Quality: 82})
	if err != nil {
		return imageopt.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return imageopt.Result{}, err
	}
	return optimized, nil
}

func zipFileEntries(zr *zip.Reader, prefix string) map[string]*zip.File {
	entries := make(map[string]*zip.File)
	for _, entry := range zr.File {
		if strings.HasPrefix(entry.Name, prefix) {
			entries[entry.Name] = entry
		}
	}
	return entries
}

func restoreNoteMediaRefs(ctx context.Context, tx pgx.Tx, uid authctx.UserID, snap *Snapshot, m idMapping) error {
	keys := make([]string, 0, len(m.noteFiles))
	owned := make(map[string]struct{}, len(m.noteFiles))
	for _, key := range m.noteFiles {
		keys = append(keys, key)
		owned[key] = struct{}{}
	}
	sort.Strings(keys)
	refs := make([]notemedia.RestoredRef, 0)
	for _, note := range snap.Notes {
		newNoteID, ok := m.noteMap[note.ID]
		if !ok {
			continue
		}
		values := []string{note.BodyHTML}
		if note.CoverURL != nil {
			values = append(values, *note.CoverURL)
		}
		for _, key := range notemedia.Keys(values...) {
			if _, ok := owned[key]; ok {
				refs = append(refs, notemedia.RestoredRef{NoteID: newNoteID, ObjectKey: key})
			}
		}
	}
	return notemedia.RestoreRefs(ctx, tx, uid, keys, refs)
}

// realignLinkImageURLs points every restored link's og_image_url at its OWN id.
//
// Link images live at an id-derived key (`/api/files/images/{id}.*`), and no
// restore mode preserves ids any more (ADR-30) — so a row inserted with the
// snapshot's og_image_url verbatim names an id that is now somebody else's row,
// or nobody's. applyFiles writes the object under the NEW id; without this pass
// the row would point at the old key, which wipe mode has just deleted. The
// result was a restore that completed "successfully" with every image broken.
//
// The rewrite needs no mapping lookup because the invariant is positional: the
// first numeric segment is always the owning row's id. Any suffix after that id
// (including the screenshot operation UUID) is preserved.
//
// Both the match and the predicate are ANCHORED to `^/api/files/`, and that
// anchor is load-bearing: og_image_url is NOT always an internal proxy path.
// preview.Fetcher stores the page's own <meta property="og:image"> verbatim, so
// the column routinely holds arbitrary external URLs — and plenty of CDNs serve
// paths like `https://cdn.example/images/1234.jpg`. An unanchored pattern would
// silently rewrite those to a local key that does not exist, turning a working
// external preview into a dead image.
func realignLinkImageURLs(ctx context.Context, tx pgx.Tx, uid authctx.UserID, m idMapping) error {
	if len(m.linkMap) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(m.linkMap))
	for _, newID := range m.linkMap {
		ids = append(ids, newID)
	}
	_, err := tx.Exec(ctx, `
        UPDATE link
        SET og_image_url = regexp_replace(og_image_url,
                                          '^/api/files/(screenshots|images)/[0-9]+\.',
                                          '/api/files/\1/' || id || '.')
        WHERE user_id = $1
          AND id = ANY($2::bigint[])
          AND og_image_url ~ '^/api/files/(screenshots|images)/[0-9]+\.'
    `, int64(uid), ids)
	if err != nil {
		return fmt.Errorf("realign link image urls: %w", err)
	}
	return nil
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
	if err := clicklog.RefreshOwner(ctx, tx, int64(uid)); err != nil {
		return err
	}
	return nil
}

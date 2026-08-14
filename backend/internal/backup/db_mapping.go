package backup

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ────────────────────────────────────────────────────────────────────────────
// ID mapping (old → new).

type idMapping struct {
	tagMap    map[int64]int64
	folderMap map[int64]int64
	linkMap   map[int64]int64
	noteMap   map[int64]int64
	noteFiles map[string]string
}

func newIDMapping() idMapping {
	return idMapping{
		tagMap:    make(map[int64]int64),
		folderMap: make(map[int64]int64),
		linkMap:   make(map[int64]int64),
		noteMap:   make(map[int64]int64),
		noteFiles: make(map[string]string),
	}
}

func (m idMapping) remapNoteFileKey(key string) (string, bool) {
	newKey, ok := m.noteFiles[key]
	return newKey, ok
}

// linkObjectID splits an id-derived object key into its parts:
// `screenshots/123.png` → ("screenshots/", 123, ".png", true).
//
// Note inline images never match: their keys are UUID-named
// (`notes/<uuid>.jpg`, written by the note image-upload endpoint) rather than
// id-named. Restore handles them through the separate fresh-UUID noteFiles map.
func linkObjectID(key string) (prefix string, id int64, suffix string, ok bool) {
	switch {
	case strings.HasPrefix(key, "screenshots/"):
		prefix = "screenshots/"
	case strings.HasPrefix(key, "images/"):
		prefix = "images/"
	default:
		return "", 0, "", false
	}
	rest := key[len(prefix):]
	dot := strings.IndexByte(rest, '.')
	if dot <= 0 {
		return "", 0, "", false
	}
	parsed, err := strconv.ParseInt(rest[:dot], 10, 64)
	if err != nil || parsed <= 0 {
		return "", 0, "", false
	}
	return prefix, parsed, rest[dot:], true
}

// remapFileKey translates an id-derived key onto the row this restore actually
// produced: `screenshots/123.png` → `screenshots/456.png` when link 123 landed
// as 456. It reports false when the key names a link the restore did NOT
// produce, and the caller must then refuse to write it.
//
// That refusal is a tenant boundary, not tidiness. Keys are flat, so writing a
// ZIP entry at its own declared key means a hand-crafted backup containing
// `files/screenshots/<someone else's link id>.jpg` would overwrite that user's
// image. Only a key derived from this restore's own id mapping is safe.
func (m idMapping) remapFileKey(key string) (string, bool) {
	prefix, oldID, suffix, ok := linkObjectID(key)
	if !ok {
		return key, false
	}
	newID, mapped := m.linkMap[oldID]
	if !mapped {
		return key, false
	}
	return prefix + strconv.FormatInt(newID, 10) + suffix, true
}

// ────────────────────────────────────────────────────────────────────────────
// scanRows is a tiny helper around pgx.Rows that calls back per row.

func scanRows(ctx context.Context, tx pgx.Tx, sql string, args []any, fn func(pgx.Rows) error) error {
	rows, err := tx.Query(ctx, sql, args...)
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

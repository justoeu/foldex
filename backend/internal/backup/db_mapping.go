package backup

import (
	"context"
	"fmt"
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

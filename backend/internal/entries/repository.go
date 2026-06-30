package entries

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/links"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// List runs one UNION ALL query across link + note so pinned-first ordering,
// sort, search, and pagination are computed by a single ORDER BY/LIMIT — see
// the package doc for why this beats a client-side merge of two independently
// paginated queries. The two arms select the SAME column list/order so the
// UNION lines up: kind, id, title, slug, pinned, folder_id, created_at,
// updated_at, click_count, last_clicked_at, url, description, favicon_url,
// og_image_url, preview_status, cover_url, body_snippet — link-only columns
// are NULL on the note arm and vice versa.
func (r *Repository) List(ctx context.Context, q ListQuery) ([]Entry, error) {
	args := []any{}
	linkWhere := []string{}
	noteWhere := []string{}

	if q.Q != "" {
		pattern := "%" + q.Q + "%"
		args = append(args, pattern)
		idx := len(args)
		linkWhere = append(linkWhere, fmt.Sprintf("(l.title ILIKE $%d OR l.url ILIKE $%d OR COALESCE(l.description,'') ILIKE $%d)", idx, idx, idx))
	}
	if len(q.TagIDs) > 0 {
		args = append(args, q.TagIDs)
		idx := len(args)
		linkWhere = append(linkWhere, fmt.Sprintf(`l.id IN (
            SELECT entity_id FROM link_tag
            WHERE entity_kind = 'link' AND tag_id = ANY($%d)
            GROUP BY entity_id
            HAVING count(DISTINCT tag_id) = %d
        )`, idx, len(q.TagIDs)))
	}
	if q.FolderID != nil {
		args = append(args, *q.FolderID)
		linkWhere = append(linkWhere, fmt.Sprintf("l.folder_id = $%d", len(args)))
	} else if q.Ungrouped {
		linkWhere = append(linkWhere, "l.folder_id IS NULL")
	}

	if q.Q != "" {
		pattern := "%" + q.Q + "%"
		args = append(args, pattern)
		idx := len(args)
		noteWhere = append(noteWhere, fmt.Sprintf("(n.title ILIKE $%d OR n.body_text ILIKE $%d)", idx, idx))
	}
	if len(q.TagIDs) > 0 {
		args = append(args, q.TagIDs)
		idx := len(args)
		noteWhere = append(noteWhere, fmt.Sprintf(`n.id IN (
            SELECT entity_id FROM link_tag
            WHERE entity_kind = 'note' AND tag_id = ANY($%d)
            GROUP BY entity_id
            HAVING count(DISTINCT tag_id) = %d
        )`, idx, len(q.TagIDs)))
	}
	if q.FolderID != nil {
		args = append(args, *q.FolderID)
		noteWhere = append(noteWhere, fmt.Sprintf("n.folder_id = $%d", len(args)))
	} else if q.Ungrouped {
		noteWhere = append(noteWhere, "n.folder_id IS NULL")
	}

	// References the UNIONed result's output column names (established by
	// the link arm's aliases — Postgres requires both arms agree, which they
	// do here since both arms select/alias every column identically).
	order := "pinned DESC, created_at DESC"
	switch q.Sort {
	case "clicks":
		order = "pinned DESC, click_count DESC, created_at DESC"
	case "recent":
		order = "pinned DESC, COALESCE(last_clicked_at, created_at) DESC"
	case "alpha":
		order = "pinned DESC, lower(title) ASC, created_at DESC"
	case "alpha_desc":
		order = "pinned DESC, lower(title) DESC, created_at DESC"
	}

	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	limitIdx := len(args) - 1
	offsetIdx := len(args)

	linkSQL := `SELECT 'link' AS kind, l.id, l.title, l.slug, l.pinned, l.folder_id, l.created_at, l.updated_at,
            COALESCE(clk.cnt, 0) AS click_count, clk.last_at AS last_clicked_at,
            l.url, l.description, l.favicon_url, l.og_image_url, l.preview_status,
            NULL::text AS cover_url, NULL::text AS body_snippet
        FROM link l
        LEFT JOIN LATERAL (
            SELECT count(*) AS cnt, max(clicked_at) AS last_at
            FROM click_log WHERE entity_kind = 'link' AND entity_id = l.id
        ) clk ON TRUE`
	if len(linkWhere) > 0 {
		linkSQL += " WHERE " + strings.Join(linkWhere, " AND ")
	}

	noteSQL := `SELECT 'note' AS kind, n.id, n.title, n.slug, n.pinned, n.folder_id, n.created_at, n.updated_at,
            COALESCE(clk.cnt, 0) AS click_count, clk.last_at AS last_clicked_at,
            NULL::text AS url, NULL::text AS description, NULL::text AS favicon_url,
            NULL::text AS og_image_url, NULL::text AS preview_status,
            n.cover_url, left(n.body_text, 240) AS body_snippet
        FROM note n
        LEFT JOIN LATERAL (
            SELECT count(*) AS cnt, max(clicked_at) AS last_at
            FROM click_log WHERE entity_kind = 'note' AND entity_id = n.id
        ) clk ON TRUE`
	if len(noteWhere) > 0 {
		noteSQL += " WHERE " + strings.Join(noteWhere, " AND ")
	}

	// Postgres forbids expressions (e.g. lower(title)) directly in an ORDER BY
	// that sits right under UNION ALL — only plain output-column references
	// are allowed there. Wrapping the union in a derived table sidesteps that
	// restriction entirely since the ORDER BY then applies to a normal
	// single-FROM query.
	sql := fmt.Sprintf("SELECT * FROM (\n%s\nUNION ALL\n%s\n) u ORDER BY %s LIMIT $%d OFFSET $%d", linkSQL, noteSQL, order, limitIdx, offsetIdx)

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	defer rows.Close()

	out := make([]Entry, 0)
	linkIDs := []int64{}
	noteIDs := []int64{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(
			&e.Kind, &e.ID, &e.Title, &e.Slug, &e.Pinned, &e.FolderID, &e.CreatedAt, &e.UpdatedAt,
			&e.ClickCount, &e.LastClickedAt,
			&e.URL, &e.Description, &e.FaviconURL, &e.OGImageURL, &e.PreviewStatus,
			&e.CoverURL, &e.BodyTextSnippet,
		); err != nil {
			return nil, err
		}
		e.Tags = []links.Tag{}
		out = append(out, e)
		if e.Kind == "link" {
			linkIDs = append(linkIDs, e.ID)
		} else {
			noteIDs = append(noteIDs, e.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(linkIDs) == 0 && len(noteIDs) == 0 {
		return out, nil
	}

	linkTags, err := r.tagsFor(ctx, "link", linkIDs)
	if err != nil {
		return nil, err
	}
	noteTags, err := r.tagsFor(ctx, "note", noteIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		var tagMap map[int64][]links.Tag
		if out[i].Kind == "link" {
			tagMap = linkTags
		} else {
			tagMap = noteTags
		}
		if t, ok := tagMap[out[i].ID]; ok {
			out[i].Tags = t
		}
	}
	return out, nil
}

// tagsFor batches a tag lookup for one entity kind — mirrors
// links.Repository.tagsFor/notes.Repository.tagsFor's shape, kept separate
// per kind (rather than one call spanning both) since link ids and note ids
// occupy the same numeric space and must never be conflated.
func (r *Repository) tagsFor(ctx context.Context, kind string, ids []int64) (map[int64][]links.Tag, error) {
	out := map[int64][]links.Tag{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
        SELECT lt.entity_id, t.id, t.name, t.color, t.icon
        FROM link_tag lt
        JOIN tag t ON t.id = lt.tag_id
        WHERE lt.entity_kind = $1 AND lt.entity_id = ANY($2)
        ORDER BY t.name ASC
    `, kind, ids)
	if err != nil {
		return nil, fmt.Errorf("tags for entries (%s): %w", kind, err)
	}
	defer rows.Close()
	for rows.Next() {
		var entityID int64
		var t links.Tag
		if err := rows.Scan(&entityID, &t.ID, &t.Name, &t.Color, &t.Icon); err != nil {
			return nil, err
		}
		out[entityID] = append(out[entityID], t)
	}
	return out, rows.Err()
}

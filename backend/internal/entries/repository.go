package entries

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/pkg/authctx"
	"foldex/internal/tags"
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
func (r *Repository) List(ctx context.Context, uid authctx.UserID, q ListQuery) ([]Entry, error) {
	args := []any{}
	linkWhere := []string{}
	noteWhere := []string{}
	appendScopeFilters(&linkWhere, &args, uid, "l", "link", q, true)
	appendScopeFilters(&noteWhere, &args, uid, "n", "note", q, false)

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

	// Pre-aggregate click_log once per entity_kind instead of LATERAL count(*)
	// per row (which forces full candidate evaluation before LIMIT on
	// sort=clicks|recent).
	linkSQL := `SELECT 'link' AS kind, l.id, l.title, l.slug, l.pinned, l.folder_id, l.created_at, l.updated_at,
            COALESCE(clk.cnt, 0) AS click_count, clk.last_at AS last_clicked_at,
            l.url, l.description, l.favicon_url, l.og_image_url, l.preview_status, l.preview_error,
            l.check_interval, l.last_checked_at, l.last_fingerprint, l.last_change_detected_at,
            l.change_seen_at, l.last_check_error,
            NULL::text AS cover_url, NULL::text AS body_snippet
        FROM link l
        LEFT JOIN (
            SELECT entity_id, count(*)::bigint AS cnt, max(clicked_at) AS last_at
            FROM click_log WHERE entity_kind = 'link'
            GROUP BY entity_id
        ) clk ON clk.entity_id = l.id`
	if len(linkWhere) > 0 {
		linkSQL += " WHERE " + strings.Join(linkWhere, " AND ")
	}

	noteSQL := `SELECT 'note' AS kind, n.id, n.title, n.slug, n.pinned, n.folder_id, n.created_at, n.updated_at,
            COALESCE(clk.cnt, 0) AS click_count, clk.last_at AS last_clicked_at,
            NULL::text AS url, NULL::text AS description, NULL::text AS favicon_url,
            NULL::text AS og_image_url, NULL::text AS preview_status, NULL::text AS preview_error,
            NULL::text AS check_interval, NULL::timestamptz AS last_checked_at, NULL::text AS last_fingerprint,
            NULL::timestamptz AS last_change_detected_at, NULL::timestamptz AS change_seen_at,
            NULL::text AS last_check_error,
            n.cover_url, left(n.body_text, 240) AS body_snippet
        FROM note n
        LEFT JOIN (
            SELECT entity_id, count(*)::bigint AS cnt, max(clicked_at) AS last_at
            FROM click_log WHERE entity_kind = 'note'
            GROUP BY entity_id
        ) clk ON clk.entity_id = n.id`
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
			&e.URL, &e.Description, &e.FaviconURL, &e.OGImageURL, &e.PreviewStatus, &e.PreviewError,
			&e.CheckInterval, &e.LastCheckedAt, &e.LastFingerprint, &e.LastChangeDetectedAt,
			&e.ChangeSeenAt, &e.LastCheckError,
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

	byKind, err := tags.TagsForLinkAndNote(ctx, r.pool, uid, linkIDs, noteIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if t, ok := byKind[out[i].Kind][out[i].ID]; ok {
			out[i].Tags = t
		}
	}
	return out, nil
}

// appendScopeFilters adds Q/TagIDs/FolderID/Ungrouped predicates for one UNION
// arm. linkSearch=true uses title|url|description; notes use title|body_text.
// appendScopeFilters builds one arm's WHERE. uid is appended FIRST so the
// tenant predicate leads and the (user_id, …) composite indexes from migration
// 000017 can be used.
//
// Note the two arms each get their OWN $n for user_id: placeholder indices come
// from len(*args) and the arms are built by separate calls into ONE shared args
// slice, so they must never assume a fixed offset.
func appendScopeFilters(where *[]string, args *[]any, uid authctx.UserID, alias, kind string, q ListQuery, linkSearch bool) {
	*args = append(*args, int64(uid))
	*where = append(*where, fmt.Sprintf("%s.user_id = $%d", alias, len(*args)))

	if q.Q != "" {
		pattern := "%" + q.Q + "%"
		*args = append(*args, pattern)
		idx := len(*args)
		if linkSearch {
			*where = append(*where, fmt.Sprintf("(%s.title ILIKE $%d OR %s.url ILIKE $%d OR COALESCE(%s.description,'') ILIKE $%d)", alias, idx, alias, idx, alias, idx))
		} else {
			*where = append(*where, fmt.Sprintf("(%s.title ILIKE $%d OR %s.body_text ILIKE $%d)", alias, idx, alias, idx))
		}
	}
	if len(q.TagIDs) > 0 {
		*args = append(*args, q.TagIDs)
		idx := len(*args)
		*where = append(*where, fmt.Sprintf(`%s.id IN (
            SELECT entity_id FROM link_tag
            WHERE entity_kind = '%s' AND tag_id = ANY($%d)
            GROUP BY entity_id
            HAVING count(DISTINCT tag_id) = %d
        )`, alias, kind, idx, len(q.TagIDs)))
	}
	if q.FolderID != nil {
		*args = append(*args, *q.FolderID)
		*where = append(*where, fmt.Sprintf("%s.folder_id = $%d", alias, len(*args)))
	} else if q.Ungrouped {
		*where = append(*where, alias+".folder_id IS NULL")
	} else {
		// Unscoped grid: never surface content from password-protected folders.
		*where = append(*where, folders.SQLNotInLockedFolder(alias))
	}
}

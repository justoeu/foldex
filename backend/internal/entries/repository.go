package entries

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/folders"
	"foldex/internal/links"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/listquery"
	"foldex/internal/tags"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Counts(ctx context.Context, uid authctx.UserID) (EntryCounts, error) {
	var counts EntryCounts
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM link WHERE user_id = $1),
			(SELECT count(*) FROM note WHERE user_id = $1)
	`, int64(uid)).Scan(&counts.Links, &counts.Notes)
	if err != nil {
		return EntryCounts{}, fmt.Errorf("count entries: %w", err)
	}
	return counts, nil
}

func buildListQuery(uid authctx.UserID, q ListQuery) (string, []any) {
	planner := listquery.NewPlanner(q)
	linkScope := planner.AddScope(uid, listquery.LinkEntity(folders.SQLNotInLockedFolder("l")))
	noteScope := planner.AddScope(uid, listquery.NoteEntity(folders.SQLNotInLockedFolder("n")))
	page := planner.AddPage(listquery.UnionOrder())

	if page.ClickRanking {
		linkSQL := fmt.Sprintf(`SELECT 'link' AS kind, l.id, l.title, l.slug, l.pinned, l.folder_id, l.created_at, l.updated_at,
            COALESCE(clk.cnt, 0) AS click_count, clk.last_at AS last_clicked_at,
            l.url, l.description, l.favicon_url, l.og_image_url, l.preview_status, l.preview_error,
            l.check_interval, l.last_checked_at, l.last_fingerprint, l.last_change_detected_at,
            l.change_seen_at, l.last_check_error,
            NULL::text AS cover_url, NULL::text AS body_snippet
        FROM link l
        LEFT JOIN (
            SELECT entity_id, count(*)::bigint AS cnt, max(clicked_at) AS last_at
			FROM click_log WHERE user_id = $%d AND entity_kind = 'link'
            GROUP BY entity_id
		) clk ON clk.entity_id = l.id`, linkScope.OwnerArg)
		linkSQL += " WHERE " + strings.Join(linkScope.Where, " AND ")

		noteSQL := fmt.Sprintf(`SELECT 'note' AS kind, n.id, n.title, n.slug, n.pinned, n.folder_id, n.created_at, n.updated_at,
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
			FROM click_log WHERE user_id = $%d AND entity_kind = 'note'
            GROUP BY entity_id
		) clk ON clk.entity_id = n.id`, noteScope.OwnerArg)
		noteSQL += " WHERE " + strings.Join(noteScope.Where, " AND ")

		sql := fmt.Sprintf("SELECT * FROM (\n%s\nUNION ALL\n%s\n) u ORDER BY %s LIMIT $%d OFFSET $%d", linkSQL, noteSQL, page.OrderBy, page.LimitArg, page.OffsetArg)
		return sql, planner.Args()
	}

	linkSQL := `SELECT 'link' AS kind, l.id, l.title, l.slug, l.pinned, l.folder_id, l.created_at, l.updated_at,
            l.url, l.description, l.favicon_url, l.og_image_url, l.preview_status, l.preview_error,
            l.check_interval, l.last_checked_at, l.last_fingerprint, l.last_change_detected_at,
            l.change_seen_at, l.last_check_error,
            NULL::text AS cover_url, NULL::text AS body_snippet
        FROM link l`
	linkSQL += " WHERE " + strings.Join(linkScope.Where, " AND ")

	noteSQL := `SELECT 'note' AS kind, n.id, n.title, n.slug, n.pinned, n.folder_id, n.created_at, n.updated_at,
            NULL::text AS url, NULL::text AS description, NULL::text AS favicon_url,
            NULL::text AS og_image_url, NULL::text AS preview_status, NULL::text AS preview_error,
            NULL::text AS check_interval, NULL::timestamptz AS last_checked_at, NULL::text AS last_fingerprint,
            NULL::timestamptz AS last_change_detected_at, NULL::timestamptz AS change_seen_at,
            NULL::text AS last_check_error,
            n.cover_url, left(n.body_text, 240) AS body_snippet
        FROM note n`
	noteSQL += " WHERE " + strings.Join(noteScope.Where, " AND ")

	// Postgres forbids expressions (e.g. lower(title)) directly in an ORDER BY
	// that sits right under UNION ALL — only plain output-column references
	// are allowed there. Wrapping the union in a derived table sidesteps that
	// restriction entirely since the ORDER BY then applies to a normal
	// single-FROM query.
	sql := fmt.Sprintf(`WITH candidates AS MATERIALIZED (
        SELECT * FROM (
%s
        UNION ALL
%s
        ) mixed
        ORDER BY %s
        LIMIT $%d OFFSET $%d
    ), page_clicks AS (
        SELECT 'link'::text AS entity_kind, cl.entity_id,
               count(*)::bigint AS cnt, max(cl.clicked_at) AS last_at
        FROM click_log cl
        WHERE cl.user_id = $%d
          AND cl.entity_kind = 'link'
          AND cl.entity_id = ANY (
              ARRAY(SELECT c.id FROM candidates c WHERE c.kind = 'link')
          )
        GROUP BY cl.entity_id
        UNION ALL
        SELECT 'note'::text AS entity_kind, cl.entity_id,
               count(*)::bigint AS cnt, max(cl.clicked_at) AS last_at
        FROM click_log cl
        WHERE cl.user_id = $%d
          AND cl.entity_kind = 'note'
          AND cl.entity_id = ANY (
              ARRAY(SELECT c.id FROM candidates c WHERE c.kind = 'note')
          )
        GROUP BY cl.entity_id
    )
    SELECT c.kind, c.id, c.title, c.slug, c.pinned, c.folder_id, c.created_at, c.updated_at,
           COALESCE(pc.cnt, 0) AS click_count, pc.last_at AS last_clicked_at,
           c.url, c.description, c.favicon_url, c.og_image_url, c.preview_status, c.preview_error,
           c.check_interval, c.last_checked_at, c.last_fingerprint, c.last_change_detected_at,
           c.change_seen_at, c.last_check_error, c.cover_url, c.body_snippet
    FROM candidates c
    LEFT JOIN page_clicks pc ON pc.entity_kind = c.kind AND pc.entity_id = c.id
    ORDER BY %s`,
		linkSQL, noteSQL, page.OrderBy, page.LimitArg, page.OffsetArg,
		linkScope.OwnerArg, noteScope.OwnerArg, page.OrderBy,
	)
	return sql, planner.Args()
}

// List uses one mixed link+note page and one batched tag query. Click-ranked
// sorts aggregate before pagination because the aggregate determines rank;
// other sorts aggregate only the selected candidate IDs.
func (r *Repository) List(ctx context.Context, uid authctx.UserID, q ListQuery) ([]Entry, error) {
	sql, args := buildListQuery(uid, q)

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

// PreviewStatuses resolves a bounded caller-supplied batch in one query. The
// LEFT JOIN deliberately emits one row per requested ID while exposing link
// fields only when the row is both owned and visible in the requested scope.
func (r *Repository) PreviewStatuses(ctx context.Context, uid authctx.UserID, ids []int64, folderID *int64) ([]PreviewStatus, error) {
	scope := folders.SQLNotInLockedFolder("l")
	args := []any{int64(uid), ids}
	if folderID != nil {
		scope = "l.folder_id = $3"
		args = append(args, *folderID)
	}
	query := fmt.Sprintf(`
        SELECT requested.id,
               l.id IS NOT NULL,
               l.preview_status,
               l.description,
               l.favicon_url,
               l.og_image_url,
               l.preview_error,
               l.updated_at
        FROM unnest($2::bigint[]) WITH ORDINALITY AS requested(id, ord)
        LEFT JOIN link l
          ON l.user_id = $1
         AND l.id = requested.id
         AND %s
        ORDER BY requested.ord
    `, scope)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("preview statuses: %w", err)
	}
	defer rows.Close()

	out := make([]PreviewStatus, 0, len(ids))
	for rows.Next() {
		var status PreviewStatus
		if err := rows.Scan(
			&status.ID,
			&status.Found,
			&status.Status,
			&status.Description,
			&status.FaviconURL,
			&status.OGImageURL,
			&status.PreviewError,
			&status.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, rows.Err()
}

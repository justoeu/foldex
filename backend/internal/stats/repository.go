package stats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/folders"
	"foldex/internal/pkg/authctx"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Dashboard builds every database-backed dashboard section from one owned-link
// and click snapshot. MATERIALIZED keeps the planner from re-reading the base
// click set independently for summary, daily, top-link, host, and tag results.
func (r *Repository) Dashboard(ctx context.Context, uid authctx.UserID, days, limit int) (Dashboard, error) {
	if days <= 0 || days > 365 {
		days = 60
	}
	if limit <= 0 || limit > 100 {
		limit = 5
	}
	query := fmt.Sprintf(`
        WITH owned_links AS MATERIALIZED (
            SELECT l.id, l.url, l.title, l.slug, l.folder_id, l.created_at
            FROM link l
            WHERE l.user_id = $1 AND %s
        ),
        base_clicks AS MATERIALIZED (
            SELECT c.entity_id, c.clicked_at
            FROM click_log c
            JOIN owned_links l ON l.id = c.entity_id
            WHERE c.user_id = $1
              AND c.entity_kind = 'link'
        ),
        clicks_by_link AS MATERIALIZED (
            SELECT entity_id,
                   count(*)::bigint AS clicks,
                   count(*) FILTER (WHERE clicked_at >= now() - interval '30 days')::bigint AS clicks_30d,
                   count(*) FILTER (
                       WHERE clicked_at < now() - interval '30 days'
                         AND clicked_at >= now() - interval '60 days'
                   )::bigint AS clicks_prev_30d
            FROM base_clicks
            GROUP BY entity_id
        ),
        host_totals AS (
            SELECT regexp_replace(l.url, '^https?://([^/]+).*$', '\1') AS host,
                   sum(c.clicks)::bigint AS clicks
            FROM owned_links l
            JOIN clicks_by_link c ON c.entity_id = l.id
            GROUP BY 1
        ),
        top_host AS (
            SELECT host, clicks
            FROM host_totals
            WHERE host <> ''
            ORDER BY clicks DESC, host ASC
            LIMIT 1
        ),
        summary AS (
            SELECT
                (SELECT count(*)::bigint FROM owned_links) AS total_links,
                (SELECT count(*)::bigint FROM tag WHERE user_id = $1) AS total_tags,
                (SELECT count(*)::bigint FROM base_clicks) AS total_clicks,
                (SELECT count(*)::bigint FROM base_clicks WHERE clicked_at >= now() - interval '30 days') AS clicks_last_30d,
                (SELECT count(*)::bigint FROM base_clicks
                    WHERE clicked_at < now() - interval '30 days'
                      AND clicked_at >= now() - interval '60 days') AS clicks_prev_30d,
                (SELECT count(*)::bigint FROM owned_links WHERE created_at >= now() - interval '30 days') AS new_links_last_30d,
                COALESCE((SELECT host FROM top_host), '') AS top_host,
                COALESCE((SELECT clicks FROM top_host), 0)::bigint AS top_host_clicks
        ),
        daily_series AS (
            SELECT generate_series(
                date_trunc('day', now()) - ($2::int - 1) * interval '1 day',
                date_trunc('day', now()),
                interval '1 day'
            ) AS day
        ),
        daily_counts AS (
            SELECT date_trunc('day', clicked_at) AS day, count(*)::bigint AS clicks
            FROM base_clicks
            WHERE clicked_at >= date_trunc('day', now()) - ($2::int - 1) * interval '1 day'
            GROUP BY 1
        ),
        daily_points AS (
            SELECT s.day, COALESCE(c.clicks, 0)::bigint AS clicks
            FROM daily_series s
            LEFT JOIN daily_counts c USING (day)
        ),
        top_rows AS (
            SELECT l.id, l.url, l.title, l.slug,
                   regexp_replace(l.url, '^https?://([^/]+).*$', '\1') AS host,
                   COALESCE(c.clicks, 0)::bigint AS clicks,
                   COALESCE(c.clicks_30d, 0)::bigint AS clicks_30d,
                   COALESCE(c.clicks_prev_30d, 0)::bigint AS clicks_prev_30d
            FROM owned_links l
            LEFT JOIN clicks_by_link c ON c.entity_id = l.id
            ORDER BY clicks DESC, l.id ASC
            LIMIT $3
        ),
        tag_rows AS (
            SELECT t.id, t.name, t.color,
                   COALESCE(sum(c.clicks), 0)::bigint AS clicks,
                   count(DISTINCT l.id)::bigint AS links
            FROM tag t
            LEFT JOIN link_tag lt ON lt.tag_id = t.id AND lt.entity_kind = 'link'
            LEFT JOIN owned_links l ON l.id = lt.entity_id
            LEFT JOIN clicks_by_link c ON c.entity_id = l.id
            WHERE t.user_id = $1
            GROUP BY t.id
        )
        SELECT summary.total_links,
               summary.total_tags,
               summary.total_clicks,
               summary.clicks_last_30d,
               summary.clicks_prev_30d,
               summary.new_links_last_30d,
               summary.top_host,
               summary.top_host_clicks,
               COALESCE((
                   SELECT jsonb_agg(jsonb_build_object('date', day::date::text, 'clicks', clicks) ORDER BY day)
                   FROM daily_points
               ), '[]'::jsonb),
               COALESCE((
                   SELECT jsonb_agg(to_jsonb(top_rows) ORDER BY clicks DESC, id ASC)
                   FROM top_rows
               ), '[]'::jsonb),
               COALESCE((
                   SELECT jsonb_agg(to_jsonb(tag_rows) ORDER BY clicks DESC, name ASC)
                   FROM tag_rows
               ), '[]'::jsonb)
        FROM summary
    `, folders.SQLNotInLockedFolder("l"))

	var out Dashboard
	var dailyJSON, topJSON, tagsJSON []byte
	if err := r.pool.QueryRow(ctx, query, int64(uid), days, limit).Scan(
		&out.Summary.TotalLinks,
		&out.Summary.TotalTags,
		&out.Summary.TotalClicks,
		&out.Summary.ClicksLast30d,
		&out.Summary.ClicksPrev30d,
		&out.Summary.NewLinksLast30,
		&out.Summary.TopHost,
		&out.Summary.TopHostClicks,
		&dailyJSON,
		&topJSON,
		&tagsJSON,
	); err != nil {
		return out, fmt.Errorf("dashboard query: %w", err)
	}
	var dailyRows []struct {
		Date   string `json:"date"`
		Clicks int64  `json:"clicks"`
	}
	if err := json.Unmarshal(dailyJSON, &dailyRows); err != nil {
		return out, fmt.Errorf("decode dashboard daily: %w", err)
	}
	out.Daily = make([]DailyPoint, len(dailyRows))
	for i, row := range dailyRows {
		date, err := time.Parse("2006-01-02", row.Date)
		if err != nil {
			return out, fmt.Errorf("decode dashboard date: %w", err)
		}
		out.Daily[i] = DailyPoint{Date: date, Clicks: row.Clicks}
	}
	if err := json.Unmarshal(topJSON, &out.Top); err != nil {
		return out, fmt.Errorf("decode dashboard top: %w", err)
	}
	if err := json.Unmarshal(tagsJSON, &out.Tags); err != nil {
		return out, fmt.Errorf("decode dashboard tags: %w", err)
	}
	return out, nil
}

// Summary collects the headline KPIs the stats page needs in one round-trip.
func (r *Repository) Summary(ctx context.Context, uid authctx.UserID) (Summary, error) {
	var s Summary

	// click_log is polymorphic (entity_kind/entity_id) — every clause here
	// filters entity_kind = 'link' so the stats page keeps its pre-notes
	// meaning (link clicks only, not note views).
	//
	// Both the denormalized click owner and the visible link join are required:
	// the former rejects cross-owner rows, while the latter drops polymorphic
	// orphans and links hidden behind a folder password.
	visibleLinks := folders.SQLNotInLockedFolder("l")
	if err := r.pool.QueryRow(ctx, fmt.Sprintf(`
        WITH visible_links AS MATERIALIZED (
            SELECT l.id, l.url, l.created_at
            FROM link l
            WHERE l.user_id = $1 AND %s
        ),
        visible_clicks AS MATERIALIZED (
            SELECT c.entity_id, c.clicked_at
            FROM click_log c
            JOIN visible_links l ON l.id = c.entity_id
            WHERE c.user_id = $1 AND c.entity_kind = 'link'
        )
        SELECT
            (SELECT count(*) FROM visible_links),
            (SELECT count(*) FROM tag  WHERE user_id = $1),
			(SELECT count(*) FROM visible_clicks),
			(SELECT count(*) FROM visible_clicks WHERE clicked_at >= now() - interval '30 days'),
			(SELECT count(*) FROM visible_clicks WHERE clicked_at < now() - interval '30 days'
			                                  AND clicked_at >= now() - interval '60 days'),
			(SELECT count(*) FROM visible_links WHERE created_at >= now() - interval '30 days')
	`, visibleLinks), int64(uid)).Scan(&s.TotalLinks, &s.TotalTags, &s.TotalClicks, &s.ClicksLast30d, &s.ClicksPrev30d, &s.NewLinksLast30); err != nil {
		return s, fmt.Errorf("summary scalars: %w", err)
	}

	// Top host: pre-aggregate clicks per entity once, then join link and run
	// regexp_replace once per link (not once per click_log row) — N1-NEX-007.
	err := r.pool.QueryRow(ctx, fmt.Sprintf(`
        WITH visible_links AS MATERIALIZED (
            SELECT l.id, l.url
            FROM link l
            WHERE l.user_id = $1 AND %s
        ),
        link_clicks AS (
			SELECT c.entity_id, count(*)::bigint AS cnt
			FROM click_log c
			JOIN visible_links l ON l.id = c.entity_id
			WHERE c.user_id = $1 AND c.entity_kind = 'link'
			GROUP BY c.entity_id
        )
        SELECT host, sum(cnt)::bigint
        FROM (
            SELECT regexp_replace(l.url, '^https?://([^/]+).*$', '\1') AS host, lc.cnt
            FROM link_clicks lc
			JOIN visible_links l ON l.id = lc.entity_id
        ) t
        WHERE host <> ''
        GROUP BY host
        ORDER BY 2 DESC
        LIMIT 1
    `, visibleLinks), int64(uid)).Scan(&s.TopHost, &s.TopHostClicks)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("top host: %w", err)
	}
	return s, nil
}

// Daily returns one bucket per day for the past `days` days (inclusive), in
// ascending date order. Days with no clicks are emitted with Clicks=0 so the
// frontend doesn't have to backfill.
func (r *Repository) Daily(ctx context.Context, uid authctx.UserID, days int) ([]DailyPoint, error) {
	if days <= 0 || days > 365 {
		days = 60
	}
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		WITH visible_links AS MATERIALIZED (
			SELECT l.id FROM link l WHERE l.user_id = $2 AND %s
		),
		series AS (
            SELECT generate_series(
                date_trunc('day', now()) - ($1::int - 1) * interval '1 day',
                date_trunc('day', now()),
                interval '1 day'
            )::date AS d
        ),
        agg AS (
            SELECT date_trunc('day', clicked_at)::date AS d, count(*)::bigint AS c
			FROM click_log c
			JOIN visible_links l ON l.id = c.entity_id
			WHERE c.user_id = $2 AND c.entity_kind = 'link'
			  AND c.clicked_at >= date_trunc('day', now()) - ($1::int - 1) * interval '1 day'
            GROUP BY 1
        )
        SELECT s.d, COALESCE(a.c, 0)
        FROM series s LEFT JOIN agg a USING (d)
        ORDER BY s.d ASC
	`, folders.SQLNotInLockedFolder("l")), days, int64(uid))
	if err != nil {
		return nil, fmt.Errorf("daily query: %w", err)
	}
	defer rows.Close()
	out := make([]DailyPoint, 0, days)
	for rows.Next() {
		var p DailyPoint
		var d time.Time
		if err := rows.Scan(&d, &p.Clicks); err != nil {
			return nil, err
		}
		p.Date = d
		out = append(out, p)
	}
	return out, rows.Err()
}

// TopLinks ranks links by total clicks in the lifetime, but also includes the
// 30d / previous-30d windows so the UI can render a delta arrow.
func (r *Repository) TopLinks(ctx context.Context, uid authctx.UserID, limit int) ([]TopLink, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	// Aggregate click_log once, then join links — avoids hashing every click
	// row against every link before LIMIT.
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		WITH visible_links AS MATERIALIZED (
			SELECT l.id, l.url, l.title, l.slug
			FROM link l
			WHERE l.user_id = $2 AND %s
		),
		link_clicks AS (
            SELECT entity_id,
                   count(*)::bigint AS clicks,
                   COALESCE(sum(CASE WHEN clicked_at >= now() - interval '30 days' THEN 1 END), 0)::bigint AS c30,
                   COALESCE(sum(CASE WHEN clicked_at <  now() - interval '30 days'
                                     AND clicked_at >= now() - interval '60 days' THEN 1 END), 0)::bigint AS cprev
			FROM click_log c
			JOIN visible_links l ON l.id = c.entity_id
			WHERE c.user_id = $2 AND c.entity_kind = 'link'
			GROUP BY c.entity_id
        )
        SELECT
            l.id, l.url, l.title, l.slug,
            regexp_replace(l.url, '^https?://([^/]+).*$', '\1') AS host,
            COALESCE(lc.clicks, 0) AS clicks,
            COALESCE(lc.c30, 0) AS c30,
            COALESCE(lc.cprev, 0) AS cprev
		FROM visible_links l
		LEFT JOIN link_clicks lc ON lc.entity_id = l.id
		ORDER BY clicks DESC, l.id ASC
		LIMIT $1
	`, folders.SQLNotInLockedFolder("l")), limit, int64(uid))
	if err != nil {
		return nil, fmt.Errorf("top links: %w", err)
	}
	defer rows.Close()
	out := []TopLink{}
	for rows.Next() {
		var t TopLink
		if err := rows.Scan(&t.ID, &t.URL, &t.Title, &t.Slug, &t.Host, &t.Clicks, &t.Clicks30d, &t.ClicksPrev); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TagBuckets returns each tag with its click total (summed across all linked
// links) and how many links it covers, ordered by clicks DESC.
//
// The naive "LEFT JOIN link_tag LEFT JOIN click_log GROUP BY t.id" runs at
// O(tags × links_per_tag × clicks_per_link) — for a power user with 10k
// clicks across 50 tags that's a fan-out of millions of intermediate rows.
// The CTE below pre-aggregates clicks per link ONCE, then joins, dropping the
// total cost to O(clicks) for the aggregate + O(link_tag rows) for the join.
func (r *Repository) TagBuckets(ctx context.Context, uid authctx.UserID) ([]TagBucket, error) {
	// link_tag/click_log are polymorphic — entity_id values overlap between
	// link and note id spaces, so every join here MUST filter
	// entity_kind = 'link' or a tag attached to a note could silently join
	// against an unrelated link/click row that happens to share the same id.
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		WITH visible_links AS MATERIALIZED (
			SELECT l.id FROM link l WHERE l.user_id = $1 AND %s
		),
		link_clicks AS (
			SELECT entity_id, count(*)::bigint AS cnt
			FROM click_log c
			JOIN visible_links l ON l.id = c.entity_id
			WHERE c.user_id = $1 AND c.entity_kind = 'link'
			GROUP BY c.entity_id
        )
        SELECT t.id, t.name, t.color,
               COALESCE(sum(lc.cnt), 0)::bigint     AS clicks,
			   count(DISTINCT l.id)::bigint AS links
		FROM tag t
		LEFT JOIN link_tag lt   ON lt.tag_id = t.id AND lt.entity_kind = 'link'
		LEFT JOIN visible_links l ON l.id = lt.entity_id
		LEFT JOIN link_clicks lc ON lc.entity_id = l.id
        WHERE t.user_id = $1
        GROUP BY t.id
        ORDER BY clicks DESC, t.name ASC
	`, folders.SQLNotInLockedFolder("l")), int64(uid))
	if err != nil {
		return nil, fmt.Errorf("tag buckets: %w", err)
	}
	defer rows.Close()
	out := []TagBucket{}
	for rows.Next() {
		var b TagBucket
		if err := rows.Scan(&b.ID, &b.Name, &b.Color, &b.Clicks, &b.Links); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

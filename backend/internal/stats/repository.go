package stats

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Summary collects the headline KPIs the stats page needs in one round-trip.
func (r *Repository) Summary(ctx context.Context) (Summary, error) {
	var s Summary

	// click_log is polymorphic (entity_kind/entity_id) — every clause here
	// filters entity_kind = 'link' so the stats page keeps its pre-notes
	// meaning (link clicks only, not note views).
	if err := r.pool.QueryRow(ctx, `
        SELECT
            (SELECT count(*) FROM link),
            (SELECT count(*) FROM tag),
            (SELECT count(*) FROM click_log WHERE entity_kind = 'link'),
            (SELECT count(*) FROM click_log WHERE entity_kind = 'link' AND clicked_at >= now() - interval '30 days'),
            (SELECT count(*) FROM click_log WHERE entity_kind = 'link' AND clicked_at <  now() - interval '30 days'
                                              AND clicked_at >= now() - interval '60 days'),
            (SELECT count(*) FROM link      WHERE created_at >= now() - interval '30 days')
    `).Scan(&s.TotalLinks, &s.TotalTags, &s.TotalClicks, &s.ClicksLast30d, &s.ClicksPrev30d, &s.NewLinksLast30); err != nil {
		return s, fmt.Errorf("summary scalars: %w", err)
	}

	// Top host by click count over the lifetime of the data. The host is a
	// derived column extracted at read time. Counts come from click_log
	// since `link.click_count` no longer exists.
	err := r.pool.QueryRow(ctx, `
        SELECT host, count(*)::bigint
        FROM (
            SELECT regexp_replace(l.url, '^https?://([^/]+).*$', '\1') AS host
            FROM click_log cl
            JOIN link l ON l.id = cl.entity_id
            WHERE cl.entity_kind = 'link'
        ) t
        WHERE host <> ''
        GROUP BY host
        ORDER BY 2 DESC
        LIMIT 1
    `).Scan(&s.TopHost, &s.TopHostClicks)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("top host: %w", err)
	}
	return s, nil
}

// Daily returns one bucket per day for the past `days` days (inclusive), in
// ascending date order. Days with no clicks are emitted with Clicks=0 so the
// frontend doesn't have to backfill.
func (r *Repository) Daily(ctx context.Context, days int) ([]DailyPoint, error) {
	if days <= 0 || days > 365 {
		days = 60
	}
	rows, err := r.pool.Query(ctx, `
        WITH series AS (
            SELECT generate_series(
                date_trunc('day', now()) - ($1::int - 1) * interval '1 day',
                date_trunc('day', now()),
                interval '1 day'
            )::date AS d
        ),
        agg AS (
            SELECT date_trunc('day', clicked_at)::date AS d, count(*)::bigint AS c
            FROM click_log
            WHERE entity_kind = 'link'
              AND clicked_at >= date_trunc('day', now()) - ($1::int - 1) * interval '1 day'
            GROUP BY 1
        )
        SELECT s.d, COALESCE(a.c, 0)
        FROM series s LEFT JOIN agg a USING (d)
        ORDER BY s.d ASC
    `, days)
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
func (r *Repository) TopLinks(ctx context.Context, limit int) ([]TopLink, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := r.pool.Query(ctx, `
        SELECT
            l.id, l.url, l.title, l.slug,
            regexp_replace(l.url, '^https?://([^/]+).*$', '\1') AS host,
            count(cl.id)::bigint AS clicks,
            COALESCE(sum(CASE WHEN cl.clicked_at >= now() - interval '30 days' THEN 1 END), 0)::bigint AS c30,
            COALESCE(sum(CASE WHEN cl.clicked_at <  now() - interval '30 days'
                              AND cl.clicked_at >= now() - interval '60 days' THEN 1 END), 0)::bigint AS cprev
        FROM link l
        LEFT JOIN click_log cl ON cl.entity_kind = 'link' AND cl.entity_id = l.id
        GROUP BY l.id
        ORDER BY clicks DESC, l.id ASC
        LIMIT $1
    `, limit)
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
func (r *Repository) TagBuckets(ctx context.Context) ([]TagBucket, error) {
	// link_tag/click_log are polymorphic — entity_id values overlap between
	// link and note id spaces, so every join here MUST filter
	// entity_kind = 'link' or a tag attached to a note could silently join
	// against an unrelated link/click row that happens to share the same id.
	rows, err := r.pool.Query(ctx, `
        WITH link_clicks AS (
            SELECT entity_id, count(*)::bigint AS cnt
            FROM click_log
            WHERE entity_kind = 'link'
            GROUP BY entity_id
        )
        SELECT t.id, t.name, t.color,
               COALESCE(sum(lc.cnt), 0)::bigint     AS clicks,
               count(DISTINCT lt.entity_id)::bigint AS links
        FROM tag t
        LEFT JOIN link_tag lt   ON lt.tag_id = t.id AND lt.entity_kind = 'link'
        LEFT JOIN link_clicks lc ON lc.entity_id = lt.entity_id
        GROUP BY t.id
        ORDER BY clicks DESC, t.name ASC
    `)
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

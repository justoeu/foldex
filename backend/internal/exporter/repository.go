package exporter

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

// linkRow is one exported link with denormalized tag names and folder name.
type linkRow struct {
	URL         string
	Title       string
	Slug        string
	Description *string
	CreatedAt   time.Time
	ClickCount  int64
	TagNames    []string
	FolderName  *string
}

type tagRow struct {
	Name  string
	Color string
	Icon  *string
}

type folderRow struct {
	Name  string
	Color string
}

// Repository loads export data (SQL stays out of HTTP handlers).
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// ListAllLinks returns every non-locked-folder link for export.
func (r *Repository) ListAllLinks(ctx context.Context, uid authctx.UserID) ([]linkRow, error) {
	rows, err := r.pool.Query(ctx, `
        WITH link_clicks AS (
            SELECT entity_id, count(*)::bigint AS cnt
            FROM click_log
            WHERE entity_kind = 'link'
              AND entity_id IN (SELECT id FROM link WHERE user_id = $1)
            GROUP BY entity_id
        )
        SELECT l.url, l.title, l.slug, l.description, l.created_at,
               COALESCE(lc.cnt, 0) AS click_count,
               COALESCE(array_agg(t.name) FILTER (WHERE t.name IS NOT NULL), '{}'),
               f.name AS folder_name
        FROM link l
        LEFT JOIN link_clicks lc ON lc.entity_id = l.id
        LEFT JOIN link_tag lt ON lt.entity_kind = 'link' AND lt.entity_id = l.id
        LEFT JOIN tag t       ON t.id = lt.tag_id
        LEFT JOIN folder f    ON f.id = l.folder_id
        WHERE l.user_id = $1 AND (l.folder_id IS NULL OR f.password_hash IS NULL)
        GROUP BY l.id, f.name, f.password_hash, lc.cnt
        ORDER BY l.created_at ASC
    `, int64(uid))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []linkRow{}
	for rows.Next() {
		var l linkRow
		if err := rows.Scan(&l.URL, &l.Title, &l.Slug, &l.Description, &l.CreatedAt, &l.ClickCount, &l.TagNames, &l.FolderName); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *Repository) ListTags(ctx context.Context, uid authctx.UserID) ([]tagRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT name, color, icon FROM tag WHERE user_id = $1 ORDER BY name`, int64(uid))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tagRow{}
	for rows.Next() {
		var t tagRow
		if err := rows.Scan(&t.Name, &t.Color, &t.Icon); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) ListFolders(ctx context.Context, uid authctx.UserID) ([]folderRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT name, color FROM folder WHERE user_id = $1 ORDER BY name`, int64(uid))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []folderRow{}
	for rows.Next() {
		var f folderRow
		if err := rows.Scan(&f.Name, &f.Color); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

package tags

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, uid authctx.UserID, in CreateInput) (Tag, error) {
	var t Tag
	err := r.pool.QueryRow(ctx, `
        INSERT INTO tag (user_id, name, color, icon)
        VALUES ($1, $2, $3, $4)
        RETURNING id, name, color, icon, created_at
    `, int64(uid), in.Name, in.Color, in.Icon).Scan(&t.ID, &t.Name, &t.Color, &t.Icon, &t.CreatedAt)
	if err != nil {
		return Tag{}, createError(err)
	}
	return t, nil
}

func createError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameTaken
	}
	return fmt.Errorf("insert tag: %w", err)
}

func (r *Repository) List(ctx context.Context, uid authctx.UserID) ([]Tag, error) {
	// link_tag is polymorphic (entity_kind/entity_id) — LinkCount keeps its
	// pre-notes meaning (links only) by filtering entity_kind='link' in the
	// join condition rather than counting note-tagged rows too.
	rows, err := r.pool.Query(ctx, `
        SELECT t.id, t.name, t.color, t.icon, t.created_at,
               COUNT(lt.entity_id) AS link_count
        FROM tag t
        LEFT JOIN link_tag lt ON lt.tag_id = t.id AND lt.entity_kind = 'link'
        WHERE t.user_id = $1
        GROUP BY t.id
        ORDER BY t.name ASC
    `, int64(uid))
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()
	out := make([]Tag, 0)
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Icon, &t.CreatedAt, &t.LinkCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns the tag owned by uid. A tag belonging to another user reports
// domainerr.ErrNotFound, never a permission error, so the id does not become a
// cross-tenant enumeration oracle.
func (r *Repository) Get(ctx context.Context, uid authctx.UserID, id int64) (Tag, error) {
	var t Tag
	err := r.pool.QueryRow(ctx, `
        SELECT id, name, color, icon, created_at
        FROM tag WHERE user_id = $1 AND id = $2
    `, int64(uid), id).Scan(&t.ID, &t.Name, &t.Color, &t.Icon, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tag{}, domainerr.ErrNotFound
	}
	if err != nil {
		return Tag{}, fmt.Errorf("get tag: %w", err)
	}
	return t, nil
}

func (r *Repository) Update(ctx context.Context, uid authctx.UserID, id int64, in UpdateInput) (Tag, error) {
	sets := []string{}
	args := []any{}
	i := 1
	if in.Name != nil {
		sets = append(sets, fmt.Sprintf("name = $%d", i))
		args = append(args, strings.TrimSpace(*in.Name))
		i++
	}
	if in.Color != nil {
		sets = append(sets, fmt.Sprintf("color = $%d", i))
		args = append(args, strings.TrimSpace(*in.Color))
		i++
	}
	if in.Icon != nil {
		sets = append(sets, fmt.Sprintf("icon = $%d", i))
		args = append(args, *in.Icon)
		i++
	}
	if len(sets) == 0 {
		return r.Get(ctx, uid, id)
	}
	args = append(args, int64(uid), id)
	q := fmt.Sprintf(`UPDATE tag SET %s WHERE user_id = $%d AND id = $%d
                      RETURNING id, name, color, icon, created_at`, strings.Join(sets, ", "), i, i+1)
	var t Tag
	err := r.pool.QueryRow(ctx, q, args...).Scan(&t.ID, &t.Name, &t.Color, &t.Icon, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tag{}, domainerr.ErrNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Tag{}, ErrNameTaken
		}
		return Tag{}, fmt.Errorf("update tag: %w", err)
	}
	return t, nil
}

func (r *Repository) Delete(ctx context.Context, uid authctx.UserID, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM tag WHERE user_id = $1 AND id = $2`, int64(uid), id)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domainerr.ErrNotFound
	}
	return nil
}

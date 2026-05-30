package folders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/httperr"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, in CreateInput) (Folder, error) {
	var f Folder
	err := r.pool.QueryRow(ctx, `
        INSERT INTO folder (name, color, parent_id)
        VALUES ($1, $2, $3)
        RETURNING id, name, color, parent_id, created_at
    `, in.Name, in.Color, in.ParentID).Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt)
	if err != nil {
		return Folder{}, fmt.Errorf("insert folder: %w", err)
	}
	f.Previews = []PreviewTile{}
	f.PreviewFolders = []PreviewFolder{}
	return f, nil
}

// ListQuery filters the folder list by hierarchical position. Default
// (zero value) returns every folder flat — useful for the link-dialog
// picker that needs to surface anything.
//
//	RootOnly = true       → only folders with parent_id IS NULL
//	ParentID = &N         → only folders whose parent_id = N
//	both zero/false       → no scoping, flat list
type ListQuery struct {
	RootOnly bool
	ParentID *int64
}

// List returns every folder matching the query, with link_count and up to 4
// preview tiles. Preview is built via LATERAL + jsonb_agg in a single
// round-trip (no N+1). Sort inside each preview: pinned first, then recent.
func (r *Repository) List(ctx context.Context, q ListQuery) ([]Folder, error) {
	where := ""
	args := []any{}
	if q.ParentID != nil {
		args = append(args, *q.ParentID)
		where = "WHERE f.parent_id = $1"
	} else if q.RootOnly {
		where = "WHERE f.parent_id IS NULL"
	}
	// link_count / folder_count via LATERAL scoped by FK instead of a
	// whole-table GROUP BY subquery — the planner can use the
	// link_folder / folder_parent indexes per parent row instead of
	// hash-aggregating every link/folder once per request.
	sql := `
        SELECT f.id, f.name, f.color, f.parent_id, f.created_at,
               COALESCE(c.cnt, 0) AS link_count,
               COALESCE(fc.cnt, 0) AS folder_count,
               COALESCE(p.previews, '[]'::jsonb) AS previews,
               COALESCE(pf.previews, '[]'::jsonb) AS preview_folders
        FROM folder f
        LEFT JOIN LATERAL (
            SELECT count(*) AS cnt FROM link WHERE folder_id = f.id
        ) c ON true
        LEFT JOIN LATERAL (
            SELECT count(*) AS cnt FROM folder WHERE parent_id = f.id
        ) fc ON true
        LEFT JOIN LATERAL (
            SELECT jsonb_agg(jsonb_build_object(
                'id', l.id, 'title', l.title,
                'og_image_url', l.og_image_url, 'favicon_url', l.favicon_url
            )) AS previews
            FROM (
                SELECT id, title, og_image_url, favicon_url
                FROM link
                WHERE folder_id = f.id
                ORDER BY pinned DESC, created_at DESC
                LIMIT 4
            ) l
        ) p ON true
        LEFT JOIN LATERAL (
            SELECT jsonb_agg(jsonb_build_object(
                'id', sf.id, 'name', sf.name, 'color', sf.color
            )) AS previews
            FROM (
                SELECT id, name, color
                FROM folder
                WHERE parent_id = f.id
                ORDER BY created_at DESC
                LIMIT 4
            ) sf
        ) pf ON true
        ` + where + `
        ORDER BY f.name ASC
    `
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer rows.Close()
	out := make([]Folder, 0)
	for rows.Next() {
		var f Folder
		var previewsJSON []byte
		var previewFoldersJSON []byte
		if err := rows.Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &f.LinkCount, &f.FolderCount, &previewsJSON, &previewFoldersJSON); err != nil {
			return nil, err
		}
		if len(previewsJSON) > 0 {
			if err := json.Unmarshal(previewsJSON, &f.Previews); err != nil {
				return nil, fmt.Errorf("unmarshal previews: %w", err)
			}
		}
		if f.Previews == nil {
			f.Previews = []PreviewTile{}
		}
		if len(previewFoldersJSON) > 0 {
			if err := json.Unmarshal(previewFoldersJSON, &f.PreviewFolders); err != nil {
				return nil, fmt.Errorf("unmarshal preview_folders: %w", err)
			}
		}
		if f.PreviewFolders == nil {
			f.PreviewFolders = []PreviewFolder{}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (r *Repository) Get(ctx context.Context, id int64) (Folder, error) {
	var f Folder
	err := r.pool.QueryRow(ctx, `
        SELECT id, name, color, parent_id, created_at
        FROM folder WHERE id = $1
    `, id).Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Folder{}, httperr.ErrNotFound
	}
	if err != nil {
		return Folder{}, fmt.Errorf("get folder: %w", err)
	}
	f.Previews = []PreviewTile{}
	f.PreviewFolders = []PreviewFolder{}
	return f, nil
}

func (r *Repository) Update(ctx context.Context, id int64, in UpdateInput) (Folder, error) {
	sets := []string{}
	args := []any{}
	i := 1
	if in.Name != nil {
		sets = append(sets, fmt.Sprintf("name = $%d", i))
		args = append(args, *in.Name)
		i++
	}
	if in.Color != nil {
		sets = append(sets, fmt.Sprintf("color = $%d", i))
			args = append(args, *in.Color)
		i++
	}

	// parent_id reassignment needs a tx so the cycle check and the UPDATE see
	// the same snapshot. A naive check-then-update on the pool let another
	// request slip a move between the two reads and create A→B→A in spite of
	// the guard. SERIALIZABLE isolation is the simplest correct fix here:
	// concurrent moves either serialize cleanly or one of them is retried.
	cycleCheckNeeded := in.ParentIDSet && in.ParentID != nil
	if in.ParentIDSet {
		if in.ParentID != nil && *in.ParentID == id {
			return Folder{}, fmt.Errorf("parent_id cannot equal id")
		}
		sets = append(sets, fmt.Sprintf("parent_id = $%d", i))
		args = append(args, in.ParentID)
		i++
	}
	if len(sets) == 0 {
		return r.Get(ctx, id)
	}
	args = append(args, id)
	q := fmt.Sprintf(`UPDATE folder SET %s WHERE id = $%d
                      RETURNING id, name, color, parent_id, created_at`, strings.Join(sets, ", "), i)

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Folder{}, fmt.Errorf("begin update tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if cycleCheckNeeded {
		var cycles bool
		err := tx.QueryRow(ctx, `
            WITH RECURSIVE ancestors AS (
                SELECT id, parent_id FROM folder WHERE id = $1
                UNION ALL
                SELECT f.id, f.parent_id
                FROM folder f
                JOIN ancestors a ON a.parent_id = f.id
            )
            SELECT EXISTS(SELECT 1 FROM ancestors WHERE id = $2)
        `, *in.ParentID, id).Scan(&cycles)
		if err != nil {
			return Folder{}, fmt.Errorf("cycle check: %w", err)
		}
		if cycles {
			// Typed 409 so the API client sees a clean conflict (extension /
			// frontend handle "user picked a descendant as parent"). Without
			// this, httperr.Write fell through to 500 and the UI couldn't tell
			// the user-fixable case apart from a real server error.
			return Folder{}, httperr.New(409, "parent_cycle", "parent_id would create a folder cycle")
		}
	}

	var f Folder
	err = tx.QueryRow(ctx, q, args...).Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Folder{}, httperr.ErrNotFound
	}
	if err != nil {
		return Folder{}, fmt.Errorf("update folder: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Folder{}, fmt.Errorf("commit update folder: %w", err)
	}
	f.Previews = []PreviewTile{}
	f.PreviewFolders = []PreviewFolder{}
	return f, nil
}

// Delete removes the folder. ON DELETE SET NULL in the FK makes every contained
// link survive — `link.folder_id` flips back to NULL.
func (r *Repository) Delete(ctx context.Context, id int64) error {
	ct, err := r.pool.Exec(ctx, `DELETE FROM folder WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete folder: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	return nil
}

// DeleteCascade removes the folder AND every link inside it — recursively
// through any subfolder tree. Wrapped in a transaction so a failure on any
// step rolls back together. `link_tag` and `click_log` rows for the deleted
// links clean up automatically via their existing `ON DELETE CASCADE` FKs;
// tags themselves survive (only the link-side associations vanish).
//
// The recursive CTE collects every descendant folder id (including the
// target), then deletes their links and finally the folders themselves.
func (r *Repository) DeleteCascade(ctx context.Context, id int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin cascade delete tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
        WITH RECURSIVE subtree AS (
          SELECT id FROM folder WHERE id = $1
          UNION ALL
          SELECT f.id FROM folder f
          JOIN subtree s ON f.parent_id = s.id
        )
        DELETE FROM link WHERE folder_id IN (SELECT id FROM subtree)
    `, id); err != nil {
		return fmt.Errorf("delete links in subtree: %w", err)
	}
	ct, err := tx.Exec(ctx, `
        WITH RECURSIVE subtree AS (
          SELECT id FROM folder WHERE id = $1
          UNION ALL
          SELECT f.id FROM folder f
          JOIN subtree s ON f.parent_id = s.id
        )
        DELETE FROM folder WHERE id IN (SELECT id FROM subtree)
    `, id)
	if err != nil {
		return fmt.Errorf("delete folder subtree: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	return tx.Commit(ctx)
}

package folders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	var passwordHash *string
	if in.Password != nil {
		h, err := HashPassword(*in.Password)
		if err != nil {
			return Folder{}, fmt.Errorf("hash password: %w", err)
		}
		passwordHash = &h
	}

	var f Folder
	var scannedHash *string
	err := r.pool.QueryRow(ctx, `
        INSERT INTO folder (name, color, parent_id, password_hash)
        VALUES ($1, $2, $3, $4)
        RETURNING id, name, color, parent_id, created_at, password_hash
    `, in.Name, in.Color, in.ParentID, passwordHash).Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &scannedHash)
	if err != nil {
		return Folder{}, fmt.Errorf("insert folder: %w", err)
	}
	f.HasPassword = scannedHash != nil
	f.Previews = []PreviewTile{}
	f.PreviewFolders = []PreviewFolder{}
	return f, nil
}

// PasswordHashFor returns the folder's current password_hash (nil when the
// folder is unprotected). Kept separate from Get so the unlock endpoint and
// the content-gate checks in this package's List and internal/entries' List
// don't pay for the preview-aggregation LATERAL joins just to check a lock
// state.
func (r *Repository) PasswordHashFor(ctx context.Context, id int64) (*string, error) {
	var hash *string
	err := r.pool.QueryRow(ctx, `SELECT password_hash FROM folder WHERE id = $1`, id).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httperr.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get folder password hash: %w", err)
	}
	return hash, nil
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
        SELECT f.id, f.name, f.color, f.parent_id, f.created_at, f.password_hash,
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
		var passwordHash *string
		var previewsJSON []byte
		var previewFoldersJSON []byte
		if err := rows.Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &passwordHash, &f.LinkCount, &f.FolderCount, &previewsJSON, &previewFoldersJSON); err != nil {
			return nil, err
		}
		f.HasPassword = passwordHash != nil
		f.Previews = []PreviewTile{}
		f.PreviewFolders = []PreviewFolder{}
		// Redaction: a protected folder's actual contents (link/subfolder
		// names, thumbnails) never leave the server via a list response,
		// regardless of whether the caller unlocked it — CheckUnlock gates
		// the SEPARATE "list what's inside" call (List(ParentID=X) or
		// entries.List(FolderID=X)), not this one. Skipping the unmarshal
		// entirely (rather than unmarshaling then discarding) also avoids
		// doing pointless work for every protected folder in a listing.
		if !f.HasPassword {
			if len(previewsJSON) > 0 {
				if err := json.Unmarshal(previewsJSON, &f.Previews); err != nil {
					return nil, fmt.Errorf("unmarshal previews: %w", err)
				}
			}
			if len(previewFoldersJSON) > 0 {
				if err := json.Unmarshal(previewFoldersJSON, &f.PreviewFolders); err != nil {
					return nil, fmt.Errorf("unmarshal preview_folders: %w", err)
				}
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (r *Repository) Get(ctx context.Context, id int64) (Folder, error) {
	var f Folder
	var passwordHash *string
	err := r.pool.QueryRow(ctx, `
        SELECT id, name, color, parent_id, created_at, password_hash
        FROM folder WHERE id = $1
    `, id).Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Folder{}, httperr.ErrNotFound
	}
	if err != nil {
		return Folder{}, fmt.Errorf("get folder: %w", err)
	}
	f.HasPassword = passwordHash != nil
	// Get never populates real preview data (only List does), so there's
	// nothing to redact here — empty arrays either way.
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

	// Hashing (pure, no DB) happens upfront; the actual authorization check
	// — "does CurrentPassword match the folder's CURRENT hash" — has to read
	// live state, so it happens inside the tx below alongside the cycle
	// check, under the same SERIALIZABLE isolation.
	var newPasswordHash *string
	if in.PasswordSet && in.Password != nil {
		h, err := HashPassword(*in.Password)
		if err != nil {
			return Folder{}, fmt.Errorf("hash new password: %w", err)
		}
		newPasswordHash = &h
	}
	if in.PasswordSet {
		sets = append(sets, fmt.Sprintf("password_hash = $%d", i))
		args = append(args, newPasswordHash)
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
                      RETURNING id, name, color, parent_id, created_at, password_hash`, strings.Join(sets, ", "), i)

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Folder{}, fmt.Errorf("begin update tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if in.PasswordSet {
		if err := checkPasswordChangeAuthorized(ctx, tx, id, in.CurrentPassword); err != nil {
			return Folder{}, err
		}
	}
	if cycleCheckNeeded {
		if err := checkParentCycle(ctx, tx, id, *in.ParentID); err != nil {
			return Folder{}, err
		}
	}

	var f Folder
	var scannedHash *string
	err = tx.QueryRow(ctx, q, args...).Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &scannedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Folder{}, httperr.ErrNotFound
	}
	if err != nil {
		return Folder{}, fmt.Errorf("update folder: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Folder{}, fmt.Errorf("commit update folder: %w", err)
	}
	f.HasPassword = scannedHash != nil
	f.Previews = []PreviewTile{}
	f.PreviewFolders = []PreviewFolder{}
	return f, nil
}

// checkPasswordChangeAuthorized enforces the CLAUDE.md-documented decision:
// changing OR removing an existing password requires proving you know the
// current one, with deliberately no admin bypass (recovery is a direct DB
// edit). Setting a password for the FIRST time (currentHash == nil) needs no
// proof — there's nothing to authorize against yet. Runs inside Update's
// SERIALIZABLE tx so the read and the eventual write share one snapshot.
func checkPasswordChangeAuthorized(ctx context.Context, tx pgx.Tx, id int64, currentPassword *string) error {
	var currentHash *string
	if err := tx.QueryRow(ctx, `SELECT password_hash FROM folder WHERE id = $1`, id).Scan(&currentHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httperr.ErrNotFound
		}
		return fmt.Errorf("read current password hash: %w", err)
	}
	if currentHash != nil {
		if currentPassword == nil || !VerifyPassword(*currentHash, *currentPassword) {
			return httperr.New(http.StatusUnauthorized, "wrong_password", "current password is required to change or remove an existing password")
		}
	}
	return nil
}

// checkParentCycle guards against a reassignment that would create a
// folder→...→folder cycle (e.g. moving A under its own descendant B).
// Runs inside Update's SERIALIZABLE tx so the check and the eventual UPDATE
// see the same snapshot — a naive check-then-update on the pool let another
// request slip a move between the two reads and create the cycle anyway.
func checkParentCycle(ctx context.Context, tx pgx.Tx, id, newParentID int64) error {
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
    `, newParentID, id).Scan(&cycles)
	if err != nil {
		return fmt.Errorf("cycle check: %w", err)
	}
	if cycles {
		// Typed 409 so the API client sees a clean conflict (extension /
		// frontend handle "user picked a descendant as parent"). Without
		// this, httperr.Write fell through to 500 and the UI couldn't tell
		// the user-fixable case apart from a real server error.
		return httperr.New(409, "parent_cycle", "parent_id would create a folder cycle")
	}
	return nil
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
// links are purged explicitly below — migration 000014 polymorphized both
// tables and DROPPED their FK to link(id) (a polymorphic column can't
// reference two tables), so the `ON DELETE CASCADE` this comment used to
// describe no longer exists; cleanup is app-level now, same as
// links.Repository.Delete. Tags themselves survive (only the link-side
// associations vanish).
//
// The recursive CTE collects every descendant folder id (including the
// target), then purges link_tag/click_log for their links, deletes the
// links, and finally the folders themselves.
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
        DELETE FROM link_tag WHERE entity_kind = 'link' AND entity_id IN (
          SELECT l.id FROM link l WHERE l.folder_id IN (SELECT id FROM subtree)
        )
    `, id); err != nil {
		return fmt.Errorf("delete link_tag for links in subtree: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        WITH RECURSIVE subtree AS (
          SELECT id FROM folder WHERE id = $1
          UNION ALL
          SELECT f.id FROM folder f
          JOIN subtree s ON f.parent_id = s.id
        )
        DELETE FROM click_log WHERE entity_kind = 'link' AND entity_id IN (
          SELECT l.id FROM link l WHERE l.folder_id IN (SELECT id FROM subtree)
        )
    `, id); err != nil {
		return fmt.Errorf("delete click_log for links in subtree: %w", err)
	}
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

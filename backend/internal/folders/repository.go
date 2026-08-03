package folders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/httperr"
)

// maxSerializationRetries bounds SERIALIZABLE update retries on SQLSTATE 40001
// (RACE-HER-008). Concurrent nest moves under load surface as transparent
// retries instead of 500 to the client.
const maxSerializationRetries = 3

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

	// A hint is meaningless without a password (it's only ever shown on the
	// unlock prompt), so drop it when the folder is unprotected.
	hint := in.PasswordHint
	if passwordHash == nil {
		hint = nil
	}

	var f Folder
	var scannedHash *string
	err := r.pool.QueryRow(ctx, `
        INSERT INTO folder (name, color, parent_id, password_hash, password_hint)
        VALUES ($1, $2, $3, $4, $5)
        RETURNING id, name, color, parent_id, created_at, password_hash, password_hint
    `, in.Name, in.Color, in.ParentID, passwordHash, hint).Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &scannedHash, &f.PasswordHint)
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
//	Slim = true           → skip preview LATERALs (and counts); metadata only
//	                        for pickers / flat tree consumers (N1-NEX-006)
type ListQuery struct {
	RootOnly bool
	ParentID *int64
	Slim     bool
}

// List returns every folder matching the query. Full mode includes link_count,
// folder_count and up to 4 preview tiles via LATERAL + jsonb_agg (RapidView).
// Slim mode returns only base columns + has_password — used by flat pickers.
func (r *Repository) List(ctx context.Context, q ListQuery) ([]Folder, error) {
	where := ""
	args := []any{}
	if q.ParentID != nil {
		args = append(args, *q.ParentID)
		where = "WHERE f.parent_id = $1"
	} else if q.RootOnly {
		where = "WHERE f.parent_id IS NULL"
	}
	if q.Slim {
		return r.listSlim(ctx, where, args)
	}
	// link_count = links + notes in the folder (card badges / cascade confirm).
	// folder_count via LATERAL scoped by FK instead of a whole-table GROUP BY.
	sql := `
        SELECT f.id, f.name, f.color, f.parent_id, f.created_at, f.password_hash, f.password_hint,
               COALESCE(c.cnt, 0) AS link_count,
               COALESCE(fc.cnt, 0) AS folder_count,
               COALESCE(p.previews, '[]'::jsonb) AS previews,
               COALESCE(pf.previews, '[]'::jsonb) AS preview_folders
        FROM folder f
        LEFT JOIN LATERAL (
            SELECT (
                (SELECT count(*) FROM link WHERE folder_id = f.id) +
                (SELECT count(*) FROM note WHERE folder_id = f.id)
            ) AS cnt
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
		if err := rows.Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &passwordHash, &f.PasswordHint, &f.LinkCount, &f.FolderCount, &previewsJSON, &previewFoldersJSON); err != nil {
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

// listSlim is the picker/flat-tree projection: base folder columns only.
// Counts and RapidView previews are zeroed — consumers that need them use
// the scoped (non-slim) List.
func (r *Repository) listSlim(ctx context.Context, where string, args []any) ([]Folder, error) {
	sql := `
        SELECT f.id, f.name, f.color, f.parent_id, f.created_at, f.password_hash, f.password_hint
        FROM folder f
        ` + where + `
        ORDER BY f.name ASC
    `
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list folders slim: %w", err)
	}
	defer rows.Close()
	out := make([]Folder, 0)
	for rows.Next() {
		var f Folder
		var passwordHash *string
		if err := rows.Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &passwordHash, &f.PasswordHint); err != nil {
			return nil, err
		}
		f.HasPassword = passwordHash != nil
		f.Previews = []PreviewTile{}
		f.PreviewFolders = []PreviewFolder{}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (r *Repository) Get(ctx context.Context, id int64) (Folder, error) {
	var f Folder
	var passwordHash *string
	err := r.pool.QueryRow(ctx, `
        SELECT id, name, color, parent_id, created_at, password_hash, password_hint
        FROM folder WHERE id = $1
    `, id).Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &passwordHash, &f.PasswordHint)
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

	// Hint handling. Removing the password (PasswordSet && Password == nil)
	// also clears any hint — a hint for a nonexistent password is dead data.
	// Otherwise apply an explicit hint change. The equality check (hint must
	// not equal the effective password) needs the folder's live hash, so it
	// runs inside the tx below via hintToValidate.
	clearHintWithPassword := in.PasswordSet && in.Password == nil
	var hintToValidate *string
	var noHint *string
	if clearHintWithPassword {
		sets = append(sets, fmt.Sprintf("password_hint = $%d", i))
		args = append(args, noHint)
		i++
	} else if in.PasswordHintSet {
		sets = append(sets, fmt.Sprintf("password_hint = $%d", i))
		args = append(args, in.PasswordHint)
		i++
		hintToValidate = in.PasswordHint
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
                      RETURNING id, name, color, parent_id, created_at, password_hash, password_hint`, strings.Join(sets, ", "), i)

	var lastErr error
	for attempt := 0; attempt < maxSerializationRetries; attempt++ {
		f, err := r.updateOnce(ctx, id, in, q, args, cycleCheckNeeded, newPasswordHash, hintToValidate)
		if err == nil {
			return f, nil
		}
		if !isSerializationFailure(err) {
			return Folder{}, err
		}
		lastErr = err
	}
	return Folder{}, lastErr
}

func (r *Repository) updateOnce(
	ctx context.Context,
	id int64,
	in UpdateInput,
	q string,
	args []any,
	cycleCheckNeeded bool,
	newPasswordHash *string,
	hintToValidate *string,
) (Folder, error) {
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
	// Hint mutation on an already-protected folder is a bcrypt oracle without
	// CurrentPassword (distinct 400 on hint==password). Require the same
	// authorization as a password change whenever the folder already has a hash.
	if in.PasswordHintSet && !in.PasswordSet {
		if err := checkPasswordChangeAuthorized(ctx, tx, id, in.CurrentPassword); err != nil {
			return Folder{}, err
		}
	}
	if cycleCheckNeeded {
		if err := checkParentCycle(ctx, tx, id, *in.ParentID); err != nil {
			return Folder{}, err
		}
	}
	if hintToValidate != nil {
		if err := checkHintNotPassword(ctx, tx, id, in.PasswordSet, newPasswordHash, *hintToValidate); err != nil {
			return Folder{}, err
		}
	}

	var f Folder
	var scannedHash *string
	err = tx.QueryRow(ctx, q, args...).Scan(&f.ID, &f.Name, &f.Color, &f.ParentID, &f.CreatedAt, &scannedHash, &f.PasswordHint)
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

// isSerializationFailure reports Postgres SQLSTATE 40001 (serialization_failure).
func isSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001"
	}
	return false
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

// checkHintNotPassword enforces the ADR-29 invariant that a folder's reminder
// hint must never equal its password. The effective password hash is the new
// one when the same request also sets a password, otherwise the folder's
// current hash (read inside the tx). A hint on a folder with no effective
// password is rejected — a hint is meaningless without a password to hint at.
func checkHintNotPassword(ctx context.Context, tx pgx.Tx, id int64, passwordSet bool, newHash *string, hint string) error {
	effHash := newHash
	if !passwordSet {
		if err := tx.QueryRow(ctx, `SELECT password_hash FROM folder WHERE id = $1`, id).Scan(&effHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httperr.ErrNotFound
			}
			return fmt.Errorf("read password hash for hint check: %w", err)
		}
	}
	if effHash == nil {
		return httperr.New(http.StatusBadRequest, "invalid_input", "cannot set a password hint on a folder without a password")
	}
	if VerifyPassword(*effHash, hint) {
		return httperr.New(http.StatusBadRequest, "invalid_input", "password hint must not be the same as the password")
	}
	return nil
}

// ResetPasswordByMaster clears a folder's password AND hint. Used by the
// master-password recovery flow (ADR-29) after the master has been verified by
// the handler. Because the unlock-token HMAC input includes the folder's
// password_hash, nulling it here invalidates every previously issued unlock
// token automatically. Returns ErrNotFound when the folder does not exist.
func (r *Repository) ResetPasswordByMaster(ctx context.Context, id int64) error {
	ct, err := r.pool.Exec(ctx, `UPDATE folder SET password_hash = NULL, password_hint = NULL WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("reset folder password: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
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

// DeleteCascade removes the folder AND every link AND note inside it —
// recursively through any subfolder tree. Wrapped in a transaction so a
// failure on any step rolls back together. `link_tag` and `click_log` rows
// for the deleted entities are purged explicitly — migration 000014
// polymorphized both tables and DROPPED their FKs, so cleanup is app-level
// (same as links.Repository.Delete / notes.Repository.Delete).
//
// Subtree ids are materialized once into a temp table (N1-NEX-008) so the
// recursive walk is not recomputed for every DML statement.
func (r *Repository) DeleteCascade(ctx context.Context, id int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin cascade delete tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
        CREATE TEMP TABLE _cascade_subtree ON COMMIT DROP AS
        WITH RECURSIVE subtree AS (
          SELECT id FROM folder WHERE id = $1
          UNION ALL
          SELECT f.id FROM folder f
          JOIN subtree s ON f.parent_id = s.id
        )
        SELECT id FROM subtree
    `, id); err != nil {
		return fmt.Errorf("materialize cascade subtree: %w", err)
	}

	if _, err := tx.Exec(ctx, `
        DELETE FROM link_tag WHERE entity_kind = 'link' AND entity_id IN (
          SELECT l.id FROM link l WHERE l.folder_id IN (SELECT id FROM _cascade_subtree)
        )
    `); err != nil {
		return fmt.Errorf("delete link_tag for links in subtree: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        DELETE FROM click_log WHERE entity_kind = 'link' AND entity_id IN (
          SELECT l.id FROM link l WHERE l.folder_id IN (SELECT id FROM _cascade_subtree)
        )
    `); err != nil {
		return fmt.Errorf("delete click_log for links in subtree: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        DELETE FROM link_tag WHERE entity_kind = 'note' AND entity_id IN (
          SELECT n.id FROM note n WHERE n.folder_id IN (SELECT id FROM _cascade_subtree)
        )
    `); err != nil {
		return fmt.Errorf("delete link_tag for notes in subtree: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        DELETE FROM click_log WHERE entity_kind = 'note' AND entity_id IN (
          SELECT n.id FROM note n WHERE n.folder_id IN (SELECT id FROM _cascade_subtree)
        )
    `); err != nil {
		return fmt.Errorf("delete click_log for notes in subtree: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        DELETE FROM link WHERE folder_id IN (SELECT id FROM _cascade_subtree)
    `); err != nil {
		return fmt.Errorf("delete links in subtree: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        DELETE FROM note WHERE folder_id IN (SELECT id FROM _cascade_subtree)
    `); err != nil {
		return fmt.Errorf("delete notes in subtree: %w", err)
	}
	ct, err := tx.Exec(ctx, `
        DELETE FROM folder WHERE id IN (SELECT id FROM _cascade_subtree)
    `)
	if err != nil {
		return fmt.Errorf("delete folder subtree: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return httperr.ErrNotFound
	}
	return tx.Commit(ctx)
}

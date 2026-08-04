package importer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/cssvalid"
	"foldex/internal/pkg/httperr"
	slugpkg "foldex/internal/pkg/slug"
	"foldex/internal/ports"

	"foldex/internal/pkg/authctx"
)

// defaultImportColor mirrors the indigo the DTO layer defaults to when a
// create/update omits color. Kept local so the importer stays self-contained.
const defaultImportColor = "#6366F1"

// sanitizeImportColor delegates to cssvalid.Sanitize so the importer shares
// the single trust-boundary helper with the backup restore path. Defense-in-
// depth for the apply path: Validate() already rejects bad colors, but
// ensureFolder/importJSON are also reachable directly and a tracking-pixel
// color (CLAUDE.md §4) must never reach the DB.
func sanitizeImportColor(c string) string {
	return cssvalid.Sanitize(c, defaultImportColor)
}

// dbtx is the narrow subset of methods this package needs from either a
// *pgxpool.Pool or a pgx.Tx. Lets the per-item helpers run either standalone
// (pool, opens its own tx as needed) or inside a caller-managed transaction
// (so importItemsWithMode can wrap the whole loop atomically).
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Handler struct {
	pool   *pgxpool.Pool
	worker ports.Enqueuer
}

func NewHandler(pool *pgxpool.Pool, worker ports.Enqueuer) *Handler {
	return &Handler{pool: pool, worker: worker}
}

func (h *Handler) Mount(r chi.Router) {
	r.Post("/", h.handle)
	r.Post("/validate", h.validate)
	r.Post("/apply", h.apply)
}

type result struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Wiped    int      `json:"wiped"`
	Format   string   `json:"format"`
	Mode     string   `json:"mode,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

const (
	// maxUploadBytes is the total request body ceiling (MaxBytesReader).
	maxUploadBytes = 100 << 20 // 100 MB
	// maxMultipartMemory is how much of a multipart body stays on the heap
	// before parts spill to temp files. Kept well below maxUploadBytes so a
	// large Chrome export doesn't pin ~100 MB RSS while parsing.
	maxMultipartMemory = 32 << 20 // 32 MB
	// maxImportItems caps bookmark rows materialized from Netscape/JSON.
	maxImportItems = 50_000
)

// Conflict mode for /api/import/apply. Mirrors the backup module's modes so
// the UX is consistent between "restore backup" and "import HTML".
type importMode string

const (
	modeSkip      importMode = "skip"
	modeWipe      importMode = "wipe"
	modeDuplicate importMode = "duplicate"
)

func parseMode(s string) (importMode, bool) {
	switch importMode(strings.ToLower(strings.TrimSpace(s))) {
	case modeSkip, "":
		return modeSkip, true
	case modeWipe:
		return modeWipe, true
	case modeDuplicate:
		return modeDuplicate, true
	}
	return "", false
}

// handle is the legacy single-shot import endpoint. It now shares the
// transactional importItemsWithMode(modeSkip) path with /apply (no more
// non-transactional importItems / importJSON forks).
func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
	up, err := h.parseUpload(w, r)
	if err != nil {
		return
	}
	imp, skipped, wiped, warnings, err := h.importItemsWithMode(r.Context(), authctx.MustUser(r.Context()), up.items, modeSkip, up.seed)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, result{
		Format:   up.format,
		Mode:     string(modeSkip),
		Imported: imp,
		Skipped:  skipped,
		Wiped:    wiped,
		Warnings: warnings,
	})
}

// validate parses the upload and computes conflict counts WITHOUT writing.
// Used by the frontend preview dialog to drive the mode picker + selection.
func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	up, err := h.parseUpload(w, r)
	if err != nil {
		// parseUpload already wrote the error response.
		return
	}
	rep, err := Validate(r.Context(), h.pool, authctx.MustUser(r.Context()), up.items)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	rep.Format = up.format
	httperr.JSON(w, http.StatusOK, rep)
}

// apply runs the import with an explicit conflict mode + optional folder
// exclusion list. Body shape: multipart with `file`, `format`, `mode`, and
// `exclude_folders` (CSV of folder paths to skip).
func (h *Handler) apply(w http.ResponseWriter, r *http.Request) {
	up, err := h.parseUpload(w, r)
	if err != nil {
		return
	}
	mode, ok := parseMode(r.FormValue("mode"))
	if !ok {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "bad_mode", "mode must be skip, wipe, or duplicate"))
		return
	}
	excluded := parseExcludedFolders(r.FormValue("exclude_folders"))
	items := up.items
	if len(excluded) > 0 {
		items = filterByFolder(items, excluded)
	}
	imp, skipped, wiped, warnings, err := h.importItemsWithMode(r.Context(), authctx.MustUser(r.Context()), items, mode, up.seed)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, result{
		Format:   up.format,
		Mode:     string(mode),
		Imported: imp,
		Skipped:  skipped,
		Wiped:    wiped,
		Warnings: warnings,
	})
}

// parseUpload is the parse-and-validate prefix shared by handle, validate,
// and apply. Writes the HTTP error response itself on failure so callers can
// uploadParse is the shared result of multipart parse for validate/apply/handle.
type uploadParse struct {
	items  []Item
	format string
	seed   *jsonSeed // non-nil for Foldex JSON (tag/folder colors)
}

// jsonSeed carries catalog rows from a JSON export so import can restore colors.
type jsonSeed struct {
	tags    []JSONTag
	folders []JSONFolder
}

// just `if err != nil { return }`.
func (h *Handler) parseUpload(w http.ResponseWriter, r *http.Request) (uploadParse, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "upload_too_large", "upload exceeds 100 MB limit"))
		return uploadParse{}, err
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	format := strings.ToLower(r.FormValue("format"))
	if format == "" {
		format = "netscape"
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "missing_file", "file field is required"))
		return uploadParse{}, err
	}
	defer file.Close()

	switch format {
	case "netscape":
		items, err := ParseNetscape(file)
		if err != nil {
			if errors.Is(err, ErrTooManyItems) {
				httperr.Write(w, httperr.New(http.StatusBadRequest, "too_many_items", err.Error()))
				return uploadParse{}, err
			}
			httperr.Write(w, httperr.New(http.StatusBadRequest, "parse_failed", err.Error()))
			return uploadParse{}, err
		}
		return uploadParse{items: items, format: format}, nil
	case "json":
		f, err := ParseJSON(file)
		if err != nil {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "parse_failed", err.Error()))
			return uploadParse{}, err
		}
		if len(f.Links) > maxImportItems {
			err := fmt.Errorf("%w: got %d links (max %d)", ErrTooManyItems, len(f.Links), maxImportItems)
			httperr.Write(w, httperr.New(http.StatusBadRequest, "too_many_items", err.Error()))
			return uploadParse{}, err
		}
		if err := f.Validate(); err != nil {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "validation_failed", err.Error()))
			return uploadParse{}, err
		}
		return uploadParse{
			items:  jsonToItems(f),
			format: format,
			seed:   &jsonSeed{tags: f.Tags, folders: f.Folders},
		}, nil
	default:
		err := httperr.New(http.StatusBadRequest, "unknown_format", "format must be netscape or json")
		httperr.Write(w, err)
		return uploadParse{}, err
	}
}

// jsonToItems flattens a Foldex JSON v1/v2 file into the same []Item shape the
// Netscape parser uses. Folder/tag references are by name (idempotent).
func jsonToItems(f JSONFile) []Item {
	out := make([]Item, 0, len(f.Links))
	for _, l := range f.Links {
		rawURL := strings.TrimSpace(l.URL)
		title := strings.TrimSpace(l.Title)
		if title == "" {
			title = rawURL
		}
		it := Item{
			URL:         rawURL,
			Title:       title,
			Tags:        l.Tags,
			Description: l.Description,
			ClickCount:  l.ClickCount,
		}
		if l.Folder != nil && strings.TrimSpace(*l.Folder) != "" {
			fp := strings.TrimSpace(*l.Folder)
			it.Folder = &fp
		}
		if l.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, l.CreatedAt); err == nil {
				it.CreatedAt = &t
			}
		}
		out = append(out, it)
	}
	return out
}

// parseExcludedFolders splits a comma-separated list of folder paths. Empty
// values are dropped.
func parseExcludedFolders(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out[p] = struct{}{}
	}
	return out
}

func filterByFolder(items []Item, excluded map[string]struct{}) []Item {
	if len(excluded) == 0 {
		return items
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		path := ""
		if it.Folder != nil {
			path = strings.TrimSpace(*it.Folder)
		}
		if _, skip := excluded[path]; skip {
			continue
		}
		out = append(out, it)
	}
	return out
}

// importItemsWithMode applies the parsed items to the DB using one of the
// three conflict modes. The whole loop runs in a SINGLE transaction so a
// failure mid-import rolls EVERYTHING back instead of leaving the DB
// half-updated. Wipe DELETEs colliding rows in the same tx as the INSERT.
// Duplicate falls back to skip-with-warning when URL collides (URL is
// UNIQUE; same trade-off as backup SDD).
//
// Enqueueing preview-worker jobs happens AFTER commit — emitting them before
// would race with the tx visibility (worker reads a link that doesn't exist
// yet from another connection).
func (h *Handler) importItemsWithMode(ctx context.Context, uid authctx.UserID, items []Item, mode importMode, seed *jsonSeed) (int, int, int, []string, error) {
	imported, skipped, wiped := 0, 0, 0
	warnings := []string{}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("begin import tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Preload tag/folder name→id once (avoids per-item SELECT/INSERT N+1).
	tagCache, err := loadTagNameCache(ctx, tx, uid)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	folderCache, err := loadFolderNameCache(ctx, tx, uid)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	// JSON exports carry tag colors + folder colors — seed before link inserts.
	if seed != nil {
		if err := seedJSONCatalog(ctx, tx, uid, seed, tagCache, folderCache); err != nil {
			return 0, 0, 0, nil, err
		}
	}

	freshIDs := make([]int64, 0, len(items))
	for _, it := range items {
		tagIDs, err := ensureTagsCached(ctx, tx, uid, tagCache, it.Tags)
		if err != nil {
			return imported, skipped, wiped, warnings, err
		}
		var folderID *int64
		if it.Folder != nil {
			fid, err := ensureFolderCached(ctx, tx, uid, folderCache, *it.Folder, "")
			if err != nil {
				return imported, skipped, wiped, warnings, err
			}
			folderID = &fid
		}
		id, dup, wipedHere, err := insertLinkInTx(ctx, tx, uid, it.URL, it.Title, it.Description, tagIDs, folderID, it.ClickCount, it.CreatedAt, mode == modeWipe)
		if err != nil {
			return imported, skipped, wiped, warnings, err
		}
		if wipedHere {
			wiped++
		}
		if dup {
			if mode == modeDuplicate {
				warnings = append(warnings, fmt.Sprintf("URL já existia, mantido o atual: %s", it.URL))
			}
			skipped++
			continue
		}
		imported++
		freshIDs = append(freshIDs, id)
	}

	if err := tx.Commit(ctx); err != nil {
		return imported, skipped, wiped, warnings, fmt.Errorf("commit import: %w", err)
	}
	if h.worker != nil {
		for _, id := range freshIDs {
			_ = h.worker.Enqueue(id)
		}
	}
	return imported, skipped, wiped, warnings, nil
}

func seedJSONCatalog(ctx context.Context, q dbtx, uid authctx.UserID, seed *jsonSeed, tagCache, folderCache map[string]int64) error {
	for _, t := range seed.tags {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		color := sanitizeImportColor(t.Color)
		var id int64
		err := q.QueryRow(ctx, `
            INSERT INTO tag (user_id, name, color, icon)
            VALUES ($1, $2, $3, $4)
            ON CONFLICT (user_id, name) DO UPDATE SET color = EXCLUDED.color, icon = COALESCE(EXCLUDED.icon, tag.icon)
            RETURNING id
        `, int64(uid), name, color, t.Icon).Scan(&id)
		if err != nil {
			return fmt.Errorf("seed tag %q: %w", name, err)
		}
		tagCache[name] = id
	}
	for _, fl := range seed.folders {
		if _, err := ensureFolderCached(ctx, q, uid, folderCache, fl.Name, sanitizeImportColor(fl.Color)); err != nil {
			return err
		}
	}
	return nil
}

func loadTagNameCache(ctx context.Context, q dbtx, uid authctx.UserID) (map[string]int64, error) {
	rows, err := q.Query(ctx, `SELECT id, name FROM tag WHERE user_id = $1`, int64(uid))
	if err != nil {
		return nil, fmt.Errorf("load tags: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, rows.Err()
}

func loadFolderNameCache(ctx context.Context, q dbtx, uid authctx.UserID) (map[string]int64, error) {
	rows, err := q.Query(ctx, `SELECT id, name FROM folder WHERE user_id = $1`, int64(uid))
	if err != nil {
		return nil, fmt.Errorf("load folders: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		// First id wins for duplicate names (same as ensureFolder LIMIT 1).
		if _, ok := out[name]; !ok {
			out[name] = id
		}
	}
	return out, rows.Err()
}

// ensureTagsCached resolves tag names via cache, inserting only on miss.
func ensureTagsCached(ctx context.Context, q dbtx, uid authctx.UserID, cache map[string]int64, names []string) ([]int64, error) {
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if id, ok := cache[name]; ok {
			ids = append(ids, id)
			continue
		}
		var id int64
		err := q.QueryRow(ctx, `
            INSERT INTO tag (user_id, name)
            VALUES ($1, $2)
            ON CONFLICT (user_id, name) DO UPDATE SET name = EXCLUDED.name
            RETURNING id
        `, int64(uid), name).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("ensure tag %q: %w", name, err)
		}
		cache[name] = id
		ids = append(ids, id)
	}
	return ids, nil
}

func ensureFolderCached(ctx context.Context, q dbtx, uid authctx.UserID, cache map[string]int64, name, color string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("ensureFolder: empty name")
	}
	if id, ok := cache[name]; ok {
		return id, nil
	}
	id, err := ensureFolder(ctx, q, uid, name, color)
	if err != nil {
		return 0, err
	}
	cache[name] = id
	return id, nil
}

// nextAvailableSlug returns `base` if no link uses it, else `base-2`,
// `base-3`, … Used by the importer's insertLinkInTx.
func nextAvailableSlug(ctx context.Context, q dbtx, base string) (string, error) {
	return slugpkg.UniqueAvailable(ctx, base, func(ctx context.Context, candidate string) (bool, error) {
		var exists bool
		err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM link WHERE slug = $1)`, candidate).Scan(&exists)
		return exists, err
	})
}

// ensureFolder finds-or-creates a folder by name. folder.name has no UNIQUE
// constraint (iPhone allows duplicate names) so we do a SELECT-then-INSERT
// dance: the import contract is "match existing by name; create a new row
// only when there's no match yet". An empty `color` defaults to indigo; a
// non-empty but cssvalid-invalid color ALSO defaults to indigo — the importer
// is a trust boundary (shared/edited JSON files) and a `red url("…")` color
// would otherwise become a tracking pixel on every chip render (CLAUDE.md §4).
// Accepts either *pgxpool.Pool or pgx.Tx (see ensureTags).
func ensureFolder(ctx context.Context, q dbtx, uid authctx.UserID, name, color string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("ensureFolder: empty name")
	}
	color = sanitizeImportColor(color)
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM folder WHERE user_id = $1 AND name = $2 LIMIT 1`, int64(uid), name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("lookup folder %q: %w", name, err)
	}
	if err := q.QueryRow(ctx, `
        INSERT INTO folder (user_id, name, color) VALUES ($1, $2, $3) RETURNING id
    `, int64(uid), name, color).Scan(&id); err != nil {
		return 0, fmt.Errorf("create folder %q: %w", name, err)
	}
	return id, nil
}

// insertLinkIfNew is the pool-level wrapper: opens its own tx, performs the
// upsert via insertLinkInTx, commits. Use this from per-item callers
// (importItems, importJSON) where each item is independently atomic.
func insertLinkIfNew(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, url, title string, description *string, tagIDs []int64, folderID *int64, clickCount int64, createdAt *time.Time, wipeFirst bool) (int64, bool, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, false, err
	}
	defer tx.Rollback(ctx)
	id, dup, wiped, err := insertLinkInTx(ctx, tx, uid, url, title, description, tagIDs, folderID, clickCount, createdAt, wipeFirst)
	if err != nil {
		return 0, false, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, false, err
	}
	return id, dup, wiped, nil
}

// insertLinkInTx upserts the link by URL inside a caller-managed transaction.
// Returns dup=true and the existing id when the URL was already present. Tag
// set is replaced either way. `clickCount` is materialized by inserting that
// many rows into click_log (only on fresh inserts; on conflict the historical
// count is preserved as-is). `createdAt` is used both as the link's created_at
// and as the timestamp for the synthetic click_log rows.
//
// When `wipeFirst` is true the function DELETEs any existing row with the same
// URL inside the same transaction, so a failure can never leave the link
// deleted without its replacement. Returns wiped=true when the DELETE actually
// removed a row.
func insertLinkInTx(ctx context.Context, tx pgx.Tx, uid authctx.UserID, url, title string, description *string, tagIDs []int64, folderID *int64, clickCount int64, createdAt *time.Time, wipeFirst bool) (int64, bool, bool, error) {
	wiped := false
	if wipeFirst {
		// Resolve the id first so link_tag/click_log can be purged before the
		// link row itself — migration 000014 dropped the FK ON DELETE CASCADE
		// those tables used to carry (polymorphized via entity_kind, can't
		// reference two tables), so cleanup is app-level now, same pattern as
		// links.Repository.Delete.
		var existingID int64
		err := tx.QueryRow(ctx, `SELECT id FROM link WHERE user_id = $1 AND url = $2`, int64(uid), url).Scan(&existingID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, false, fmt.Errorf("resolve wipe target %q: %w", url, err)
		}
		if err == nil {
			// existingID came from the owner-scoped SELECT above, so these two
			// unscoped child DELETEs (link_tag/click_log carry no user_id)
			// cannot reach another tenant's rows.
			if _, err := tx.Exec(ctx, `DELETE FROM link_tag WHERE entity_kind = 'link' AND entity_id = $1`, existingID); err != nil {
				return 0, false, false, fmt.Errorf("wipe link_tag %q: %w", url, err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM click_log WHERE entity_kind = 'link' AND entity_id = $1`, existingID); err != nil {
				return 0, false, false, fmt.Errorf("wipe click_log %q: %w", url, err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM link WHERE user_id = $1 AND id = $2`, int64(uid), existingID); err != nil {
				return 0, false, false, fmt.Errorf("wipe delete %q: %w", url, err)
			}
			wiped = true
		}
	}

	// Atomic upsert: ON CONFLICT DO NOTHING returns nothing on conflict, so a
	// second SELECT finds the existing id. Avoids depending on pgx's xmax
	// scanning rules (pgx 5 rejects xid → int64 in some pool configurations).
	//
	// Slug is auto-derived from title via Slugify with collision suffix
	// (importers never carry a user-supplied slug — that's a UI-time choice).
	// We resolve a free slug FIRST via SELECT against the live table, then
	// INSERT with the resolved value. A small race remains (two concurrent
	// imports targeting the same slug) — but importers are single-user single-
	// machine, and the unique constraint catches it as a hard error if it
	// ever happens.
	slugBase := slugpkg.Slugify(title)
	if slugBase == "" {
		slugBase = "link-imported"
	}
	slug, err := nextAvailableSlug(ctx, tx, slugBase)
	if err != nil {
		return 0, false, false, err
	}

	var id int64
	err = tx.QueryRow(ctx, `
        INSERT INTO link (user_id, url, title, slug, description, folder_id, created_at)
        VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7, now()))
        ON CONFLICT (user_id, url) DO NOTHING
        RETURNING id
    `, int64(uid), url, title, slug, description, folderID, createdAt).Scan(&id)
	dup := false
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, false, err
		}
		// Conflict path — fetch the existing row.
		if err := tx.QueryRow(ctx, `SELECT id FROM link WHERE user_id = $1 AND url = $2`, int64(uid), url).Scan(&id); err != nil {
			return 0, false, false, fmt.Errorf("resolve duplicate url: %w", err)
		}
		dup = true
	}

	// Restore historical click_count by inserting that many rows into click_log
	// stamped at the link's created_at (or now() if absent). Only for fresh
	// inserts — we don't want re-import to inflate counts on existing links.
	if !dup && clickCount > 0 {
		if _, err := tx.Exec(ctx, `
            INSERT INTO click_log (entity_kind, entity_id, clicked_at)
            SELECT 'link', $1, COALESCE($2::timestamptz, now())
            FROM generate_series(1, $3::int)
        `, id, createdAt, clickCount); err != nil {
			return 0, false, false, fmt.Errorf("backfill click_log: %w", err)
		}
	}

	if len(tagIDs) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM link_tag WHERE entity_kind = 'link' AND entity_id = $1`, id); err != nil {
			return 0, false, false, err
		}
		rows := make([][]any, 0, len(tagIDs))
		for _, tid := range tagIDs {
			rows = append(rows, []any{"link", id, tid})
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"link_tag"},
			[]string{"entity_kind", "entity_id", "tag_id"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return 0, false, false, err
		}
	}
	return id, dup, wiped, nil
}

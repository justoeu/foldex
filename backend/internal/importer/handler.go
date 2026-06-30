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

	"foldex/internal/links"
	"foldex/internal/pkg/cssvalid"
	"foldex/internal/pkg/httperr"
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
	worker links.Enqueuer
}

func NewHandler(pool *pgxpool.Pool, worker links.Enqueuer) *Handler {
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

const maxUploadBytes = 100 << 20 // 100 MB — bumped from 20MB; a power user Chrome export of 50k+ links easily exceeds 20MB uncompressed. Backups already cap at 2GiB, so 100MB here is the consistent middle ground.

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

func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "upload_too_large", "upload exceeds 100 MB limit"))
		return
	}
	format := strings.ToLower(r.FormValue("format"))
	if format == "" {
		format = "netscape"
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "missing_file", "file field is required"))
		return
	}
	defer file.Close()

	res := result{Format: format}
	switch format {
	case "netscape":
		items, err := ParseNetscape(file)
		if err != nil {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "parse_failed", err.Error()))
			return
		}
		imp, skipped, err := h.importItems(r.Context(), items)
		if err != nil {
			httperr.Write(w, err)
			return
		}
		res.Imported = imp
		res.Skipped = skipped
	case "json":
		f, err := ParseJSON(file)
		if err != nil {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "parse_failed", err.Error()))
			return
		}
		if err := f.Validate(); err != nil {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "validation_failed", err.Error()))
			return
		}
		imp, skipped, err := h.importJSON(r.Context(), f)
		if err != nil {
			httperr.Write(w, err)
			return
		}
		res.Imported = imp
		res.Skipped = skipped
	default:
		httperr.Write(w, httperr.New(http.StatusBadRequest, "unknown_format", "format must be netscape or json"))
		return
	}
	httperr.JSON(w, http.StatusOK, res)
}

// validate parses the upload and computes conflict counts WITHOUT writing.
// Used by the frontend preview dialog to drive the mode picker + selection.
func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	items, format, err := h.parseUpload(w, r)
	if err != nil {
		// parseUpload already wrote the error response.
		return
	}
	rep, err := Validate(r.Context(), h.pool, items)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	rep.Format = format
	httperr.JSON(w, http.StatusOK, rep)
}

// apply runs the import with an explicit conflict mode + optional folder
// exclusion list. Body shape: multipart with `file`, `format`, `mode`, and
// `exclude_folders` (CSV of folder paths to skip).
func (h *Handler) apply(w http.ResponseWriter, r *http.Request) {
	items, format, err := h.parseUpload(w, r)
	if err != nil {
		return
	}
	mode, ok := parseMode(r.FormValue("mode"))
	if !ok {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "bad_mode", "mode must be skip, wipe, or duplicate"))
		return
	}
	excluded := parseExcludedFolders(r.FormValue("exclude_folders"))
	if len(excluded) > 0 {
		items = filterByFolder(items, excluded)
	}
	imp, skipped, wiped, warnings, err := h.importItemsWithMode(r.Context(), items, mode)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, result{
		Format:   format,
		Mode:     string(mode),
		Imported: imp,
		Skipped:  skipped,
		Wiped:    wiped,
		Warnings: warnings,
	})
}

// parseUpload is the parse-and-validate prefix shared by handle, validate,
// and apply. Writes the HTTP error response itself on failure so callers can
// just `if err != nil { return }`.
func (h *Handler) parseUpload(w http.ResponseWriter, r *http.Request) ([]Item, string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "upload_too_large", "upload exceeds 100 MB limit"))
		return nil, "", err
	}
	format := strings.ToLower(r.FormValue("format"))
	if format == "" {
		format = "netscape"
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "missing_file", "file field is required"))
		return nil, "", err
	}
	defer file.Close()

	switch format {
	case "netscape":
		items, err := ParseNetscape(file)
		if err != nil {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "parse_failed", err.Error()))
			return nil, "", err
		}
		return items, format, nil
	case "json":
		f, err := ParseJSON(file)
		if err != nil {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "parse_failed", err.Error()))
			return nil, "", err
		}
		if err := f.Validate(); err != nil {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "validation_failed", err.Error()))
			return nil, "", err
		}
		// Adapt JSON file → []Item so validate/apply can share logic.
		return jsonToItems(f), format, nil
	default:
		err := httperr.New(http.StatusBadRequest, "unknown_format", "format must be netscape or json")
		httperr.Write(w, err)
		return nil, "", err
	}
}

// jsonToItems flattens a Foldex JSON v1/v2 file into the same []Item shape the
// Netscape parser uses. Folder/tag references are by name (idempotent).
func jsonToItems(f JSONFile) []Item {
	out := make([]Item, 0, len(f.Links))
	for _, l := range f.Links {
		it := Item{URL: l.URL, Title: l.Title, Tags: l.Tags}
		if l.Folder != nil && strings.TrimSpace(*l.Folder) != "" {
			fp := strings.TrimSpace(*l.Folder)
			it.Folder = &fp
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
func (h *Handler) importItemsWithMode(ctx context.Context, items []Item, mode importMode) (int, int, int, []string, error) {
	imported, skipped, wiped := 0, 0, 0
	warnings := []string{}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("begin import tx: %w", err)
	}
	defer tx.Rollback(ctx)

	freshIDs := make([]int64, 0, len(items))
	for _, it := range items {
		tagIDs, err := ensureTags(ctx, tx, it.Tags)
		if err != nil {
			return imported, skipped, wiped, warnings, err
		}
		var folderID *int64
		if it.Folder != nil {
			fid, err := ensureFolder(ctx, tx, *it.Folder, "")
			if err != nil {
				return imported, skipped, wiped, warnings, err
			}
			folderID = &fid
		}
		id, dup, wipedHere, err := insertLinkInTx(ctx, tx, it.URL, it.Title, nil, tagIDs, folderID, 0, nil, mode == modeWipe)
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

// importItems inserts links + their tags + folder. Duplicate URLs are skipped.
func (h *Handler) importItems(ctx context.Context, items []Item) (int, int, error) {
	imported, skipped := 0, 0
	for _, it := range items {
		tagIDs, err := ensureTags(ctx, h.pool, it.Tags)
		if err != nil {
			return imported, skipped, err
		}
		var folderID *int64
		if it.Folder != nil {
			fid, err := ensureFolder(ctx, h.pool, *it.Folder, "")
			if err != nil {
				return imported, skipped, err
			}
			folderID = &fid
		}
		id, dup, _, err := insertLinkIfNew(ctx, h.pool, it.URL, it.Title, nil, tagIDs, folderID, 0, nil, false)
		if err != nil {
			return imported, skipped, err
		}
		if dup {
			skipped++
			continue
		}
		imported++
		if h.worker != nil {
			_ = h.worker.Enqueue(id)
		}
	}
	return imported, skipped, nil
}

func (h *Handler) importJSON(ctx context.Context, f JSONFile) (int, int, error) {
	for _, t := range f.Tags {
		name := strings.TrimSpace(t.Name)
		color := sanitizeImportColor(t.Color)
		// color is guaranteed non-empty by sanitizeImportColor (defaults to
		// indigo on empty/invalid), so the INSERT can use it verbatim.
		_, err := h.pool.Exec(ctx, `
            INSERT INTO tag (name, color, icon)
            VALUES ($1, $2, $3)
            ON CONFLICT (name) DO NOTHING
        `, name, color, t.Icon)
		if err != nil {
			return 0, 0, fmt.Errorf("upsert tag: %w", err)
		}
	}
	for _, fl := range f.Folders {
		if _, err := ensureFolder(ctx, h.pool, fl.Name, sanitizeImportColor(fl.Color)); err != nil {
			return 0, 0, err
		}
	}
	imported, skipped := 0, 0
	for _, l := range f.Links {
		rawURL := strings.TrimSpace(l.URL)
		title := strings.TrimSpace(l.Title)
		if title == "" {
			title = rawURL
		}

		var createdAt *time.Time
		if l.CreatedAt != "" {
			t, _ := time.Parse(time.RFC3339, l.CreatedAt) // already validated
			createdAt = &t
		}

		tagIDs, err := ensureTags(ctx, h.pool, l.Tags)
		if err != nil {
			return imported, skipped, err
		}
		var folderID *int64
		if l.Folder != nil && strings.TrimSpace(*l.Folder) != "" {
			fid, err := ensureFolder(ctx, h.pool, *l.Folder, "")
			if err != nil {
				return imported, skipped, err
			}
			folderID = &fid
		}
		id, dup, _, err := insertLinkIfNew(ctx, h.pool, rawURL, title, l.Description, tagIDs, folderID, l.ClickCount, createdAt, false)
		if err != nil {
			return imported, skipped, err
		}
		if dup {
			skipped++
			continue
		}
		imported++
		if h.worker != nil {
			_ = h.worker.Enqueue(id)
		}
	}
	return imported, skipped, nil
}

// ensureTags upserts tag names and returns their IDs. Accepts either a
// *pgxpool.Pool (single-shot use from importItems / importJSON) or a pgx.Tx
// (so importItemsWithMode can wrap the whole loop in one transaction).
func ensureTags(ctx context.Context, q dbtx, names []string) ([]int64, error) {
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var id int64
		err := q.QueryRow(ctx, `
            INSERT INTO tag (name)
            VALUES ($1)
            ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
            RETURNING id
        `, name).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("ensure tag %q: %w", name, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// nextAvailableSlug returns `base` if no link uses it, else `base-2`,
// `base-3`, … (capped at 999 to prevent pathological loops). Used by the
// importer's insertLinkInTx — bulk imports of similarly-titled pages
// shouldn't fail just because the first slug already exists.
func nextAvailableSlug(ctx context.Context, q dbtx, base string) (string, error) {
	candidate := base
	for attempt := 1; attempt < 1000; attempt++ {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM link WHERE slug = $1)`, candidate).Scan(&exists); err != nil {
			return "", fmt.Errorf("check slug availability: %w", err)
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, attempt+1)
	}
	return "", fmt.Errorf("nextAvailableSlug: exhausted attempts for %q", base)
}

// ensureFolder finds-or-creates a folder by name. folder.name has no UNIQUE
// constraint (iPhone allows duplicate names) so we do a SELECT-then-INSERT
// dance: the import contract is "match existing by name; create a new row
// only when there's no match yet". An empty `color` defaults to indigo; a
// non-empty but cssvalid-invalid color ALSO defaults to indigo — the importer
// is a trust boundary (shared/edited JSON files) and a `red url("…")` color
// would otherwise become a tracking pixel on every chip render (CLAUDE.md §4).
// Accepts either *pgxpool.Pool or pgx.Tx (see ensureTags).
func ensureFolder(ctx context.Context, q dbtx, name, color string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("ensureFolder: empty name")
	}
	color = sanitizeImportColor(color)
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM folder WHERE name = $1 LIMIT 1`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("lookup folder %q: %w", name, err)
	}
	if err := q.QueryRow(ctx, `
        INSERT INTO folder (name, color) VALUES ($1, $2) RETURNING id
    `, name, color).Scan(&id); err != nil {
		return 0, fmt.Errorf("create folder %q: %w", name, err)
	}
	return id, nil
}

// insertLinkIfNew is the pool-level wrapper: opens its own tx, performs the
// upsert via insertLinkInTx, commits. Use this from per-item callers
// (importItems, importJSON) where each item is independently atomic.
func insertLinkIfNew(ctx context.Context, pool *pgxpool.Pool, url, title string, description *string, tagIDs []int64, folderID *int64, clickCount int64, createdAt *time.Time, wipeFirst bool) (int64, bool, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, false, err
	}
	defer tx.Rollback(ctx)
	id, dup, wiped, err := insertLinkInTx(ctx, tx, url, title, description, tagIDs, folderID, clickCount, createdAt, wipeFirst)
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
func insertLinkInTx(ctx context.Context, tx pgx.Tx, url, title string, description *string, tagIDs []int64, folderID *int64, clickCount int64, createdAt *time.Time, wipeFirst bool) (int64, bool, bool, error) {
	wiped := false
	if wipeFirst {
		ct, err := tx.Exec(ctx, `DELETE FROM link WHERE url = $1`, url)
		if err != nil {
			return 0, false, false, fmt.Errorf("wipe delete %q: %w", url, err)
		}
		if ct.RowsAffected() > 0 {
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
	slugBase := links.Slugify(title)
	if slugBase == "" {
		slugBase = "link-imported"
	}
	slug, err := nextAvailableSlug(ctx, tx, slugBase)
	if err != nil {
		return 0, false, false, err
	}

	var id int64
	err = tx.QueryRow(ctx, `
        INSERT INTO link (url, title, slug, description, folder_id, created_at)
        VALUES ($1, $2, $3, $4, $5, COALESCE($6, now()))
        ON CONFLICT (url) DO NOTHING
        RETURNING id
    `, url, title, slug, description, folderID, createdAt).Scan(&id)
	dup := false
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, false, err
		}
		// Conflict path — fetch the existing row.
		if err := tx.QueryRow(ctx, `SELECT id FROM link WHERE url = $1`, url).Scan(&id); err != nil {
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

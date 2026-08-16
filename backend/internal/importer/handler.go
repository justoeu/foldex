package importer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/cssvalid"
	"foldex/internal/pkg/httperr"
	"foldex/internal/ports"
	"foldex/internal/preview"
)

// defaultImportColor mirrors the indigo the DTO layer defaults to when a
// create/update omits color. Kept local so the importer stays self-contained.
const defaultImportColor = "#6366F1"

// sanitizeImportColor delegates to cssvalid.Sanitize so the importer shares
// the single trust-boundary helper with the backup restore path. Defense-in-
// depth for the staged apply path: a tracking-pixel color (CLAUDE.md §4) must
// never reach the DB even when a caller bypasses JSONFile.Validate.
func sanitizeImportColor(c string) string {
	return cssvalid.Sanitize(c, defaultImportColor)
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

func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	up, err := h.parseUpload(w, r)
	if err != nil {
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

type uploadParse struct {
	items  []Item
	format string
	seed   *jsonSeed // non-nil for Foldex JSON (tag/folder colors)
}

type jsonSeed struct {
	tags    []JSONTag
	folders []JSONFolder
}

// parseUpload writes client-facing errors so callers can return immediately.
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

// Preview jobs are enqueued after commit so workers can observe the inserted rows.
func (h *Handler) importItemsWithMode(ctx context.Context, uid authctx.UserID, items []Item, mode importMode, seed *jsonSeed) (int, int, int, []string, error) {
	if err := validateImportClickBudget(items); err != nil {
		return 0, 0, 0, nil, err
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("begin import tx: %w", err)
	}
	defer tx.Rollback(ctx)

	imported, skipped, wiped, warnings, freshIDs, err := applyStagedImport(ctx, tx, uid, items, mode, seed)
	if err != nil {
		return 0, 0, 0, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return imported, skipped, wiped, warnings, fmt.Errorf("commit import: %w", err)
	}
	if h.worker != nil {
		for i, id := range freshIDs {
			if err := h.worker.Enqueue(id); errors.Is(err, preview.ErrQueueFull) {
				warnings = append(warnings, fmt.Sprintf(
					"Fila de previews cheia; %d previews pendentes serão recuperados em segundo plano.",
					len(freshIDs)-i,
				))
				break
			}
		}
	}
	return imported, skipped, wiped, warnings, nil
}

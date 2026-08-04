package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/httperr"

	"foldex/internal/pkg/authctx"
)

// ExportReader is satisfied by *Repository.
type ExportReader interface {
	ListAllLinks(ctx context.Context, uid authctx.UserID) ([]linkRow, error)
	ListTags(ctx context.Context, uid authctx.UserID) ([]tagRow, error)
	ListFolders(ctx context.Context, uid authctx.UserID) ([]folderRow, error)
}

type Handler struct {
	repo ExportReader
}

func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{repo: NewRepository(pool)}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.export)
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "netscape"
	}
	switch format {
	case "netscape":
		h.exportNetscape(w, r)
	case "json":
		h.exportJSON(w, r)
	default:
		httperr.Write(w, httperr.New(http.StatusBadRequest, "unknown_format", "format must be netscape or json"))
	}
}

func (h *Handler) queryAll(r *http.Request) ([]linkRow, error) {
	return h.repo.ListAllLinks(r.Context(), authctx.MustUser(r.Context()))
}

func (h *Handler) exportNetscape(w http.ResponseWriter, r *http.Request) {
	all, err := h.queryAll(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="foldex-bookmarks.html"`)

	// Group by foldex folder when present, otherwise fall back to first tag.
	// Browsers' bookmark importers respect <H3> as folder boundaries — keeping
	// our folder concept aligned with Chrome's avoids data loss on round-trip.
	groups := map[string][]linkRow{}
	order := []string{}
	for _, l := range all {
		key := "Sem pasta"
		if l.FolderName != nil && *l.FolderName != "" {
			key = *l.FolderName
		} else if len(l.TagNames) > 0 {
			key = l.TagNames[0]
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], l)
	}

	fmt.Fprintln(w, "<!DOCTYPE NETSCAPE-Bookmark-file-1>")
	fmt.Fprintln(w, "<META HTTP-EQUIV=\"Content-Type\" CONTENT=\"text/html; charset=UTF-8\">")
	fmt.Fprintln(w, "<TITLE>Bookmarks</TITLE>")
	fmt.Fprintln(w, "<H1>Foldex export</H1>")
	fmt.Fprintln(w, "<DL><p>")
	for _, key := range order {
		fmt.Fprintf(w, "  <DT><H3>%s</H3>\n", html.EscapeString(key))
		fmt.Fprintln(w, "  <DL><p>")
		for _, l := range groups[key] {
			// %q emits Go-syntax quoting (\" escapes), which is NOT HTML-attribute
			// safe — a URL containing a literal `"` would break out of HREF and
			// inject markup. html.EscapeString handles `"`, `'`, `<`, `>`, `&`.
			fmt.Fprintf(w, `    <DT><A HREF="%s" ADD_DATE="%d">%s</A>`+"\n",
				html.EscapeString(l.URL), l.CreatedAt.Unix(), html.EscapeString(l.Title))
		}
		fmt.Fprintln(w, "  </DL><p>")
	}
	fmt.Fprintln(w, "</DL><p>")
}

func (h *Handler) exportJSON(w http.ResponseWriter, r *http.Request) {
	type jsonTag struct {
		Name  string  `json:"name"`
		Color string  `json:"color"`
		Icon  *string `json:"icon"`
	}
	type jsonFolder struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	type jsonLink struct {
		URL         string   `json:"url"`
		Title       string   `json:"title"`
		Slug        string   `json:"slug"`
		Description *string  `json:"description"`
		Tags        []string `json:"tags"`
		Folder      *string  `json:"folder"`
		ClickCount  int64    `json:"click_count"`
		CreatedAt   string   `json:"created_at"`
	}
	type doc struct {
		Version    int          `json:"version"`
		ExportedAt string       `json:"exported_at"`
		Tags       []jsonTag    `json:"tags"`
		Folders    []jsonFolder `json:"folders"`
		Links      []jsonLink   `json:"links"`
	}

	// Drain each query into a slice and release the connection back to the pool
	// before starting the next one. The whole point is that we don't want three
	// connections held simultaneously across the JSON encode at the end.
	tags, err := h.repo.ListTags(r.Context(), authctx.MustUser(r.Context()))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	folders, err := h.repo.ListFolders(r.Context(), authctx.MustUser(r.Context()))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	all, err := h.queryAll(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	out := doc{Version: 2, ExportedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, t := range tags {
		out.Tags = append(out.Tags, jsonTag{Name: t.Name, Color: t.Color, Icon: t.Icon})
	}
	for _, f := range folders {
		out.Folders = append(out.Folders, jsonFolder{Name: f.Name, Color: f.Color})
	}
	for _, l := range all {
		out.Links = append(out.Links, jsonLink{
			URL:         l.URL,
			Title:       l.Title,
			Slug:        l.Slug,
			Description: l.Description,
			Tags:        l.TagNames,
			Folder:      l.FolderName,
			ClickCount:  l.ClickCount,
			CreatedAt:   l.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="foldex-bookmarks.json"`)
	_ = json.NewEncoder(w).Encode(out)
}

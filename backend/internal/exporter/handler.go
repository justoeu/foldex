package exporter

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/httperr"
)

type Handler struct {
	pool *pgxpool.Pool
}

func NewHandler(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

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

type linkRow struct {
	URL         string
	Title       string
	Description *string
	CreatedAt   time.Time
	ClickCount  int64
	TagNames    []string
	FolderName  *string
}

func (h *Handler) queryAll(r *http.Request) ([]linkRow, error) {
	// click_count is derived from click_log (no longer denormalized on
	// `link`). Counting in a subquery avoids double-counting with the
	// link_tag join. Folder name comes from the LEFT JOIN — NULL when the
	// link isn't in a folder.
	rows, err := h.pool.Query(r.Context(), `
        SELECT l.url, l.title, l.description, l.created_at,
               (SELECT count(*) FROM click_log WHERE link_id = l.id)::bigint AS click_count,
               COALESCE(array_agg(t.name) FILTER (WHERE t.name IS NOT NULL), '{}'),
               f.name AS folder_name
        FROM link l
        LEFT JOIN link_tag lt ON lt.link_id = l.id
        LEFT JOIN tag t       ON t.id = lt.tag_id
        LEFT JOIN folder f    ON f.id = l.folder_id
        GROUP BY l.id, f.name
        ORDER BY l.created_at ASC
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []linkRow{}
	for rows.Next() {
		var l linkRow
		if err := rows.Scan(&l.URL, &l.Title, &l.Description, &l.CreatedAt, &l.ClickCount, &l.TagNames, &l.FolderName); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
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
			fmt.Fprintf(w, `    <DT><A HREF=%q ADD_DATE="%d">%s</A>`+"\n",
				l.URL, l.CreatedAt.Unix(), html.EscapeString(l.Title))
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
	tags, err := queryTags(r, h.pool)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	folders, err := queryFolders(r, h.pool)
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

type tagRow struct {
	Name  string
	Color string
	Icon  *string
}

type folderRow struct {
	Name  string
	Color string
}

func queryTags(r *http.Request, pool *pgxpool.Pool) ([]tagRow, error) {
	rows, err := pool.Query(r.Context(), `SELECT name, color, icon FROM tag ORDER BY name`)
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

func queryFolders(r *http.Request, pool *pgxpool.Pool) ([]folderRow, error) {
	rows, err := pool.Query(r.Context(), `SELECT name, color FROM folder ORDER BY name`)
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

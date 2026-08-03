// Package listquery parses the shared list filter query-string shape used by
// GET /api/entries, /api/links, and /api/notes.
package listquery

import (
	"net/http"
	"strconv"
	"strings"

	"foldex/internal/pkg/clampint"
)

// Params is the common filter/pagination surface for entity list endpoints.
type Params struct {
	Q         string
	TagIDs    []int64
	Sort      string
	Limit     int
	Offset    int
	FolderID  *int64
	Ungrouped bool
}

// Parse reads q/sort/tag/limit/offset/folder_id/ungrouped from r's query string.
func Parse(r *http.Request) Params {
	q := Params{
		Q:    strings.TrimSpace(r.URL.Query().Get("q")),
		Sort: r.URL.Query().Get("sort"),
	}
	for _, raw := range r.URL.Query()["tag"] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			q.TagIDs = append(q.TagIDs, id)
		}
	}
	q.Limit = clampint.Int(r.URL.Query().Get("limit"), 100, 1, 500)
	q.Offset = clampint.Int(r.URL.Query().Get("offset"), 0, 0, 100000)
	if v := r.URL.Query().Get("folder_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			q.FolderID = &n
		}
	}
	if v := r.URL.Query().Get("ungrouped"); v == "1" || v == "true" {
		q.Ungrouped = true
	}
	return q
}

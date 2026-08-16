package notes

import (
	"time"

	"foldex/internal/tags"
)

// Note mirrors links.Link's shape minus the URL-specific fields (no preview
// pipeline, no favicon, no change-detection) plus the rich-content fields.
// ClickCount/LastClickedAt are derived from click_log the same way links
// derives them — via a LATERAL join, never a denormalized column.
type Note struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Slug  string `json:"slug"`
	// BodyHTML is the full rich body on Get/Create/Update and public resolution. List
	// leaves it empty so list responses omit the field (omitempty) — clients
	// that need the editor payload must GET by id.
	BodyHTML string `json:"body_html,omitempty"`
	// BodyText is the server-derived plain-text search column — never sent
	// to clients (the frontend renders BodyHTML; BodyText only feeds
	// ILIKE/trigram search). Excluded from JSON to keep response payloads
	// from doubling the body size for no reader benefit.
	BodyText      string      `json:"-"`
	Pinned        bool        `json:"pinned"`
	FolderID      *int64      `json:"folder_id,omitempty"`
	CoverURL      *string     `json:"cover_url,omitempty"`
	ClickCount    int64       `json:"click_count"`
	LastClickedAt *time.Time  `json:"last_clicked_at,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Tags          []tags.Chip `json:"tags"`
}

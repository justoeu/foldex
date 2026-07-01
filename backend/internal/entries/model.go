// Package entries is a read-only projection over link + note, the data
// source for the interleaved home/folder grid and its search box. It never
// writes — mutations stay on /api/links and /api/notes; this package exists
// solely so the frontend can query one paginated, sorted, searched endpoint
// instead of merging two independently-paginated streams client-side. See
// ADR-27.
package entries

import (
	"time"

	"foldex/internal/links"
)

// Entry is a flat union of link + note fields with a Kind discriminator.
// Kept flat (rather than a nested {Link: ..., Note: ...} shape) because
// that's what falls out of scanning one UNION ALL row, and it serializes to
// a simple discriminated union on the frontend (kind: 'link' | 'note').
// Fields not applicable to the row's Kind are left nil/zero.
type Entry struct {
	Kind          string      `json:"kind"`
	ID            int64       `json:"id"`
	Title         string      `json:"title"`
	Slug          string      `json:"slug"`
	Pinned        bool        `json:"pinned"`
	FolderID      *int64      `json:"folder_id,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	ClickCount    int64       `json:"click_count"`
	LastClickedAt *time.Time  `json:"last_clicked_at,omitempty"`
	Tags          []links.Tag `json:"tags"`

	// link-only — nil/empty for kind="note". Mirrors links.Link's full shape
	// (including change-detection columns) so the frontend can treat a
	// kind="link" Entry as a drop-in Link for LinkCard's "Monitored" chip /
	// unseen-change badge / preview-failed indicator (CLAUDE.md §5) — those
	// invariants must not regress just because the home grid's data source
	// switched from useLinks to useEntries.
	URL                  *string    `json:"url,omitempty"`
	Description          *string    `json:"description,omitempty"`
	FaviconURL           *string    `json:"favicon_url,omitempty"`
	OGImageURL           *string    `json:"og_image_url,omitempty"`
	PreviewStatus        *string    `json:"preview_status,omitempty"`
	PreviewError         *string    `json:"preview_error,omitempty"`
	CheckInterval        *string    `json:"check_interval,omitempty"`
	LastCheckedAt        *time.Time `json:"last_checked_at,omitempty"`
	LastFingerprint      *string    `json:"last_fingerprint,omitempty"`
	LastChangeDetectedAt *time.Time `json:"last_change_detected_at,omitempty"`
	ChangeSeenAt         *time.Time `json:"change_seen_at,omitempty"`
	LastCheckError       *string    `json:"last_check_error,omitempty"`

	// note-only — nil/empty for kind="link".
	CoverURL        *string `json:"cover_url,omitempty"`
	BodyTextSnippet *string `json:"body_text_snippet,omitempty"`
}

type ListQuery struct {
	Q         string
	TagIDs    []int64
	Sort      string
	Limit     int
	Offset    int
	FolderID  *int64
	Ungrouped bool
}

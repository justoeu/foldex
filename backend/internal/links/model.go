package links

import "time"

type Tag struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Color string  `json:"color"`
	Icon  *string `json:"icon,omitempty"`
}

type Link struct {
	ID            int64      `json:"id"`
	URL           string     `json:"url"`
	Title         string     `json:"title"`
	Slug          string     `json:"slug"`
	Description   *string    `json:"description,omitempty"`
	FaviconURL    *string    `json:"favicon_url,omitempty"`
	OGImageURL    *string    `json:"og_image_url,omitempty"`
	ClickCount    int64      `json:"click_count"`
	PreviewStatus string     `json:"preview_status"`
	PreviewError  *string    `json:"preview_error,omitempty"`
	LastClickedAt *time.Time `json:"last_clicked_at,omitempty"`
	Pinned        bool       `json:"pinned"`
	FolderID      *int64     `json:"folder_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Tags          []Tag      `json:"tags"`
}

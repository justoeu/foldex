package tags

import "time"

type Tag struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	Icon      *string   `json:"icon,omitempty"`
	LinkCount int64     `json:"link_count"`
	CreatedAt time.Time `json:"created_at"`
}

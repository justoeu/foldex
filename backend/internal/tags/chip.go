package tags

// Chip is the nested tag shape embedded on links/notes/entries JSON
// (no link_count — that lives only on tags.Tag list responses).
type Chip struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Color string  `json:"color"`
	Icon  *string `json:"icon,omitempty"`
}

package folders

import "time"

type Folder struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	ParentID *int64 `json:"parent_id,omitempty"`
	// HasPassword never carries the hash itself — repository methods scan
	// folder.password_hash into a local variable and set this bool inline,
	// they never store the hash on the struct. See CheckUnlock/List's
	// redaction rule in repository.go.
	HasPassword bool `json:"has_password"`
	// PasswordHint is a NON-SECRET reminder phrase shown on the unlock prompt
	// (ADR-29). Unlike the hash it IS returned to clients — surfacing it is the
	// whole point. nil when the folder has no hint. Enforced to never equal the
	// password (see dto/repository).
	PasswordHint   *string         `json:"password_hint,omitempty"`
	LinkCount      int64           `json:"link_count"`
	FolderCount    int64           `json:"folder_count"`
	Previews       []PreviewTile   `json:"preview_links"`
	PreviewFolders []PreviewFolder `json:"preview_folders"`
	CreatedAt      time.Time       `json:"created_at"`
}

// PreviewTile is the iPhone-style mini-thumbnail used to build the 2x2 grid
// inside a FolderCard on the frontend. Up to 4 are returned per folder.
type PreviewTile struct {
	ID         int64   `json:"id"`
	Title      string  `json:"title"`
	OGImageURL *string `json:"og_image_url"`
	FaviconURL *string `json:"favicon_url"`
}

// PreviewFolder is the mini-thumbnail for a SUBFOLDER inside a parent folder.
// Used to fill the 2x2 preview grid when the parent has no direct links (or
// has both — frontend mixes them).
type PreviewFolder struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

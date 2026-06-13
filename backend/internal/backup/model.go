package backup

import "time"

// Magic discriminator written into every manifest. Reject zips that don't have
// this in `manifest.kind` — keeps random-zip-upload from doing damage.
const ManifestKind = "foldex.backup"

// ManifestVersion is the format version of the backup ZIP layout itself.
// Bump the MAJOR when changing file layout incompatibly.
const ManifestVersion = "1.0"

// SchemaVersion mirrors the latest applied DB migration. Restoring a backup
// with a higher SchemaVersion than the server's current = fatal error
// (server doesn't know the layout). Lower = warning (defaults applied).
const CurrentSchemaVersion = 8

// DatabaseSnapshotVersion is the schema of database.json itself. v3 = adds
// link_tags + click_logs to v2 (which had tags/folders/links only).
const DatabaseSnapshotVersion = 3

type Counts struct {
	Links     int64 `json:"links"`
	Tags      int64 `json:"tags"`
	Folders   int64 `json:"folders"`
	LinkTags  int64 `json:"link_tags"`
	ClickLogs int64 `json:"click_logs"`
	Files     int64 `json:"files"`
	FileBytes int64 `json:"file_bytes"`
}

type Manifest struct {
	Kind          string            `json:"kind"`
	Version       string            `json:"version"`
	SchemaVersion int               `json:"schema_version"`
	CreatedAt     time.Time         `json:"created_at"`
	FoldexVersion string            `json:"foldex_version,omitempty"`
	Counts        Counts            `json:"counts"`
	Checksums     map[string]string `json:"checksums"`
}

// Snapshot is the in-memory shape of database.json. Field names are
// snake_case JSON to match what the existing exporter/importer use.
type Snapshot struct {
	Version   int          `json:"version"`
	Tags      []TagRow     `json:"tags"`
	Folders   []FolderRow  `json:"folders"`
	Links     []LinkRow    `json:"links"`
	LinkTags  []LinkTagRow `json:"link_tags"`
	ClickLogs []ClickRow   `json:"click_logs"`
}

type TagRow struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	Icon      *string   `json:"icon"`
	CreatedAt time.Time `json:"created_at"`
}

type FolderRow struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	ParentID  *int64    `json:"parent_id"`
	CreatedAt time.Time `json:"created_at"`
}

type LinkRow struct {
	ID            int64     `json:"id"`
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	Slug          string    `json:"slug"`
	Description   *string   `json:"description"`
	FaviconURL    *string   `json:"favicon_url"`
	OGImageURL    *string   `json:"og_image_url"`
	Pinned        bool      `json:"pinned"`
	PreviewStatus string    `json:"preview_status"`
	PreviewError  *string   `json:"preview_error"`
	FolderID      *int64    `json:"folder_id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type LinkTagRow struct {
	LinkID int64 `json:"link_id"`
	TagID  int64 `json:"tag_id"`
}

type ClickRow struct {
	LinkID    int64     `json:"link_id"`
	ClickedAt time.Time `json:"clicked_at"`
}

// Validation is returned by /api/backup/validate without touching the DB.
type Validation struct {
	OK        bool      `json:"ok"`
	Manifest  *Manifest `json:"manifest,omitempty"`
	Conflicts Conflicts `json:"conflicts"`
	Warnings  []string  `json:"warnings"`
	Errors    []string  `json:"errors"`
}

type Conflicts struct {
	Links   int64 `json:"links"`
	Tags    int64 `json:"tags"`
	Folders int64 `json:"folders"`
}

// ConflictMode is the strategy for /api/backup/restore.
type ConflictMode string

const (
	ModeWipe      ConflictMode = "wipe"
	ModeSkip      ConflictMode = "skip"
	ModeDuplicate ConflictMode = "duplicate"
)

func (m ConflictMode) Valid() bool {
	switch m {
	case ModeWipe, ModeSkip, ModeDuplicate:
		return true
	default:
		return false
	}
}

type RestoreReport struct {
	Mode       ConflictMode `json:"mode"`
	Inserted   Counts       `json:"inserted"`
	Skipped    Counts       `json:"skipped"`
	Wiped      Counts       `json:"wiped"`
	Files      FileReport   `json:"files"`
	Warnings   []string     `json:"warnings"`
	DurationMs int64        `json:"duration_ms"`
}

type FileReport struct {
	Uploaded int64 `json:"uploaded"`
	Skipped  int64 `json:"skipped"`
	Wiped    int64 `json:"wiped"`
}

type ExportReport struct {
	Counts     Counts `json:"counts"`
	DurationMs int64  `json:"duration_ms"`
	SizeBytes  int64  `json:"size_bytes"`
}

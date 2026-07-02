package backup

import (
	"time"

	"foldex/internal/pkg/cssvalid"
)

// Magic discriminator written into every manifest. Reject zips that don't have
// this in `manifest.kind` — keeps random-zip-upload from doing damage.
const ManifestKind = "foldex.backup"

// ManifestVersion is the format version of the backup ZIP layout itself.
// Bump the MAJOR when changing file layout incompatibly.
const ManifestVersion = "1.0"

// SchemaVersion mirrors the latest applied DB migration. Restoring a backup
// with a higher SchemaVersion than the server's current = fatal error
// (server doesn't know the layout). Lower = warning (defaults applied).
// v10 = migration 000015 adds folder.password_hash.
// v11 = migration 000016 adds app_setting + folder.password_hint.
const CurrentSchemaVersion = 11

// DatabaseSnapshotVersion is the schema of database.json itself. v3 = adds
// link_tags + click_logs to v2 (which had tags/folders/links only). v4 = adds
// notes + note_tags + note_clicks (migration 000014 polymorphized link_tag/
// click_log via entity_kind; the JSON wire shape of existing link rows is
// unchanged). An older backup (no "notes" key) decodes fine — missing array
// fields default to nil/empty. Stays at v4 for the 000015 folder.password_hash
// addition: FolderRow just gained one more nullable field, no new top-level
// snapshot key or decoder options (no DisallowUnknownFields anywhere in this
// package) — an old backup without "password_hash" restores every folder as
// unprotected, exactly like any other missing-field default. v5 = adds
// app_settings (the master recovery password hash, ADR-29) + folder rows gain
// a password_hint field; an older backup lacking either restores with the
// setting absent / hint nil, no special casing.
const DatabaseSnapshotVersion = 5

type Counts struct {
	Links     int64 `json:"links"`
	Notes     int64 `json:"notes"`
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
//
// Notes, NoteTags, NoteClicks are kept as separate arrays (rather than
// folding into LinkTags/ClickLogs) even though both pairs ultimately write to
// the same polymorphic link_tag/click_log tables — keeping the wire format
// split by entity kind means an old backup (DatabaseSnapshotVersion < 4, no
// "notes"/"note_tags"/"note_clicks" keys) decodes with these as nil slices
// and every restore mode's note loop is simply a no-op, with zero special
// casing required.
type Snapshot struct {
	Version    int            `json:"version"`
	Tags       []TagRow       `json:"tags"`
	Folders    []FolderRow    `json:"folders"`
	Links      []LinkRow      `json:"links"`
	Notes      []NoteRow      `json:"notes"`
	LinkTags   []LinkTagRow   `json:"link_tags"`
	NoteTags   []NoteTagRow   `json:"note_tags"`
	ClickLogs  []ClickRow     `json:"click_logs"`
	NoteClicks []NoteClickRow `json:"note_clicks"`
	// AppSettings round-trips the app_setting KV table verbatim (ADR-29) —
	// currently just the master recovery password hash. Kept a separate array
	// so an old backup (DatabaseSnapshotVersion < 5, no "app_settings" key)
	// decodes as a nil slice and every restore mode's loop is a no-op.
	AppSettings []AppSettingRow `json:"app_settings"`
}

// defaultColor is the indigo the DTO layer defaults to on Create/Update. Kept
// here (not in cssvalid) so the cssvalid leaf package stays free of any
// business default — each consumer picks its own fallback.
const defaultColor = "#6366F1"

// Sanitize coerces every tag/folder color through the cssvalid allowlist,
// defaulting to indigo on empty or invalid input. The backup zip is a trust
// boundary — a shared/edited/manually-crafted snapshot can carry
// `red url("https://evil/exfil")` and turn every chip render into a tracking
// pixel (CLAUDE.md §4). Called once at load (readSnapshotFromZip) so all
// three restore modes (identity/skip/duplicate) inherit the guard for free.
// Returns the count of coerced colors so the caller can surface a warning.
func (s *Snapshot) Sanitize() int {
	coerced := 0
	for i := range s.Tags {
		before := s.Tags[i].Color
		s.Tags[i].Color = cssvalid.Sanitize(before, defaultColor)
		if s.Tags[i].Color != before {
			coerced++
		}
	}
	for i := range s.Folders {
		before := s.Folders[i].Color
		s.Folders[i].Color = cssvalid.Sanitize(before, defaultColor)
		if s.Folders[i].Color != before {
			coerced++
		}
	}
	return coerced
}

type TagRow struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	Icon      *string   `json:"icon"`
	CreatedAt time.Time `json:"created_at"`
}

type FolderRow struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	ParentID *int64 `json:"parent_id"`
	// PasswordHash is copied VERBATIM on restore — it's already a bcrypt
	// hash (or nil), never a plaintext password. Never re-hash it.
	PasswordHash *string `json:"password_hash"`
	// PasswordHint is the non-secret reminder phrase (ADR-29), copied verbatim.
	PasswordHint *string   `json:"password_hint"`
	CreatedAt    time.Time `json:"created_at"`
}

// AppSettingRow is one app_setting KV pair. value for the master_password_hash
// key is a bcrypt hash (or the row is absent) — copied verbatim on restore,
// never re-hashed.
type AppSettingRow struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
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

// NoteRow mirrors LinkRow's shape minus URL/Favicon/PreviewStatus/PreviewError
// (notes have no external resource to preview) plus the rich-content fields.
type NoteRow struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Slug      string    `json:"slug"`
	BodyHTML  string    `json:"body_html"`
	BodyText  string    `json:"body_text"`
	Pinned    bool      `json:"pinned"`
	FolderID  *int64    `json:"folder_id"`
	CoverURL  *string   `json:"cover_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type NoteTagRow struct {
	NoteID int64 `json:"note_id"`
	TagID  int64 `json:"tag_id"`
}

type NoteClickRow struct {
	NoteID    int64     `json:"note_id"`
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

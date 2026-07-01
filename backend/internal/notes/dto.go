package notes

import (
	"encoding/json"
	"fmt"
	"strings"

	"foldex/internal/pkg/htmlsanitize"
	"foldex/internal/pkg/slug"
)

// MaxTitleBytes mirrors links.MaxTitleBytes' role but is kept as its own
// constant — duplicating one int is cheaper than coupling notes to links for
// it, and the two entities are free to diverge later.
const (
	MaxTitleBytes = 500
	// MaxBodyHTMLBytes caps the body AFTER sanitization (Normalize runs
	// Sanitize before Validate checks length) — 512 KiB comfortably covers a
	// long pastebin-style note with a handful of inline images referenced by
	// URL (the images themselves live in object storage, not inline as
	// base64 — see htmlsanitize's data: URL exclusion).
	MaxBodyHTMLBytes = 512 << 10
)

type CreateInput struct {
	Title string `json:"title"`
	// Slug is optional on create — when nil/empty the repository derives it
	// from Title via slug.Slugify (with auto-suffix on collision), same as
	// links.
	Slug     *string `json:"slug"`
	BodyHTML string  `json:"body_html"`
	TagIDs   []int64 `json:"tag_ids"`
	Pinned   bool    `json:"pinned"`
	FolderID *int64  `json:"folder_id"`
}

// Normalize trims text fields and sanitizes BodyHTML server-side — the
// client's HTML is never trusted, so this runs unconditionally before
// Validate (which checks the now-sanitized length) and before the repository
// ever sees the value.
func (c *CreateInput) Normalize() {
	c.Title = strings.TrimSpace(c.Title)
	if c.Slug != nil {
		s := strings.TrimSpace(*c.Slug)
		if s == "" {
			c.Slug = nil
		} else {
			c.Slug = &s
		}
	}
	c.BodyHTML = htmlsanitize.Sanitize(c.BodyHTML)
}

func (c CreateInput) Validate() error {
	if c.Title == "" {
		return errMsg("title is required")
	}
	if len(c.Title) > MaxTitleBytes {
		return errMsg(fmt.Sprintf("title too long (max %d)", MaxTitleBytes))
	}
	if c.Slug != nil && !slug.IsValid(*c.Slug) {
		return errMsg("slug must match [a-z0-9-]+ (no leading/trailing/consecutive hyphens, not purely numeric, max 80 chars)")
	}
	if len(c.BodyHTML) > MaxBodyHTMLBytes {
		return errMsg(fmt.Sprintf("body too long (max %d bytes after sanitization)", MaxBodyHTMLBytes))
	}
	return nil
}

type UpdateInput struct {
	Title *string `json:"title"`
	// BodyHTML is a plain pointer, not tri-state — nil means "don't touch",
	// a present empty string is a legal cleared body. Sanitized in Normalize
	// the same way CreateInput.BodyHTML is.
	BodyHTML *string  `json:"body_html"`
	TagIDs   *[]int64 `json:"tag_ids"`
	Pinned   *bool    `json:"pinned"`
	// FolderID: tri-state, same contract as links.UpdateInput.FolderID —
	// absent → don't touch, {"folder_id": N} → assign, {"folder_id": null} →
	// clear.
	FolderID    *int64 `json:"-"`
	FolderIDSet bool   `json:"-"`
	// Slug: tri-state — absent → don't touch, explicit value → set,
	// {"slug": null} → regenerate from title via slug.Slugify().
	Slug    *string `json:"-"`
	SlugSet bool    `json:"-"`
}

func (u *UpdateInput) Normalize() {
	if u.Title != nil {
		s := strings.TrimSpace(*u.Title)
		u.Title = &s
	}
	if u.BodyHTML != nil {
		s := htmlsanitize.Sanitize(*u.BodyHTML)
		u.BodyHTML = &s
	}
}

func (u UpdateInput) Validate() error {
	if u.Title != nil {
		if *u.Title == "" {
			return errMsg("title is required")
		}
		if len(*u.Title) > MaxTitleBytes {
			return errMsg(fmt.Sprintf("title too long (max %d)", MaxTitleBytes))
		}
	}
	if u.BodyHTML != nil && len(*u.BodyHTML) > MaxBodyHTMLBytes {
		return errMsg(fmt.Sprintf("body too long (max %d bytes after sanitization)", MaxBodyHTMLBytes))
	}
	if u.SlugSet && u.Slug != nil && !slug.IsValid(*u.Slug) {
		return errMsg("slug must match [a-z0-9-]+ (no leading/trailing/consecutive hyphens, not purely numeric, max 80 chars)")
	}
	return nil
}

type ListQuery struct {
	Q         string
	TagIDs    []int64
	Sort      string // created|clicks|recent|alpha|alpha_desc
	Limit     int
	Offset    int
	FolderID  *int64
	Ungrouped bool
}

// UnmarshalJSON preserves the "null vs absent" distinction on folder_id and
// slug — same pattern as links.UpdateInput.UnmarshalJSON.
func (u *UpdateInput) UnmarshalJSON(data []byte) error {
	type alias UpdateInput
	aux := struct {
		FolderID json.RawMessage `json:"folder_id"`
		Slug     json.RawMessage `json:"slug"`
		*alias
	}{alias: (*alias)(u)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if len(aux.FolderID) == 0 {
		u.FolderIDSet = false
		u.FolderID = nil
	} else {
		u.FolderIDSet = true
		if string(aux.FolderID) == "null" {
			u.FolderID = nil
		} else {
			var n int64
			if err := json.Unmarshal(aux.FolderID, &n); err != nil {
				return err
			}
			u.FolderID = &n
		}
	}

	if len(aux.Slug) == 0 {
		u.SlugSet = false
		u.Slug = nil
	} else {
		u.SlugSet = true
		if string(aux.Slug) == "null" {
			u.Slug = nil
		} else {
			var s string
			if err := json.Unmarshal(aux.Slug, &s); err != nil {
				return err
			}
			s = strings.TrimSpace(s)
			if s == "" {
				u.Slug = nil
			} else {
				u.Slug = &s
			}
		}
	}
	return nil
}

type validationErr string

func (e validationErr) Error() string { return string(e) }
func errMsg(s string) error           { return validationErr(s) }

package links

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"foldex/internal/pkg/jsonopt"
	"foldex/internal/pkg/listquery"
	"foldex/internal/tags"
)

// Maximum byte lengths enforced by the Create/Update DTOs. Exported so the
// URL-metadata pre-fetch handler (and any future surface that produces text
// destined for these fields) can truncate to the same limits the Save path
// will accept — pre-fill that returns a title longer than the DTO accepts
// would be a self-inflicted UX bug (user clicks Save → 400 invalid_input).
const (
	MaxTitleBytes = 500
)

type CreateInput struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	// Slug is optional on create — when nil/empty the repository derives it
	// from Title via Slugify (with auto-suffix on collision).
	Slug        *string            `json:"slug"`
	Description *string            `json:"description"`
	TagIDs      []int64            `json:"tag_ids"`
	PendingTags []tags.CreateInput `json:"pending_tags"`
	Pinned      bool               `json:"pinned"`
	FolderID    *int64             `json:"folder_id"`
	// CheckInterval opts the link into the changecheck worker. Nil/empty =
	// disabled. Must be one of "hourly"/"daily"/"weekly" or Validate rejects.
	CheckInterval *string `json:"check_interval"`
}

func (c *CreateInput) Normalize() {
	c.URL = strings.TrimSpace(c.URL)
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" {
		c.Title = c.URL
	}
	if c.Slug != nil {
		s := strings.TrimSpace(*c.Slug)
		if s == "" {
			c.Slug = nil
		} else {
			c.Slug = &s
		}
	}
	if c.CheckInterval != nil {
		s := strings.TrimSpace(*c.CheckInterval)
		if s == "" {
			c.CheckInterval = nil
		} else {
			c.CheckInterval = &s
		}
	}
	tags.NormalizeCreateInputs(c.PendingTags)
}

func (c CreateInput) Validate() error {
	if c.URL == "" {
		return errMsg("url is required")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errMsg("url must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errMsg("url scheme must be http or https")
	}
	if len(c.Title) > MaxTitleBytes {
		return errMsg("title too long (max 500)")
	}
	if c.Slug != nil && !SlugIsValid(*c.Slug) {
		return errMsg("slug must match [a-z0-9-]+ (no leading/trailing/consecutive hyphens, not purely numeric, max 80 chars)")
	}
	if c.CheckInterval != nil && !ValidCheckInterval(*c.CheckInterval) {
		return errMsg("check_interval must be one of hourly, daily, weekly")
	}
	if err := tags.ValidateCreateInputs(c.PendingTags); err != nil {
		return errMsg(err.Error())
	}
	return nil
}

type UpdateInput struct {
	URL         *string            `json:"url"`
	Title       *string            `json:"title"`
	Description *string            `json:"description"`
	TagIDs      *[]int64           `json:"tag_ids"`
	PendingTags []tags.CreateInput `json:"pending_tags"`
	Pinned      *bool              `json:"pinned"`
	// FolderID has 3 states by intent:
	//   field absent in JSON → don't touch
	//   {"folder_id": N}     → assign to folder N
	//   {"folder_id": null}  → clear (no folder)
	// We can't disambiguate "absent" vs "null" with a `*int64` alone, so we
	// carry an explicit FolderIDSet flag that the JSON decoder flips in
	// UnmarshalJSON (see helper below).
	FolderID    *int64 `json:"-"`
	FolderIDSet bool   `json:"-"`
	// Slug shares the same tri-state pattern: absent → don't touch,
	// {"slug": "foo-bar"} → set explicitly, {"slug": null} → regenerate
	// from title via Slugify().
	Slug    *string `json:"-"`
	SlugSet bool    `json:"-"`
	// CheckInterval tri-state: absent → don't touch, {"check_interval": "daily"}
	// → set, {"check_interval": null} → opt out (clears all change-check state
	// in the repository).
	CheckInterval    *string `json:"-"`
	CheckIntervalSet bool    `json:"-"`
	// IfMatchUpdatedAt is optional optimistic concurrency (RACE-HER-012):
	// when set, UPDATE requires row.updated_at equality; 0 rows → 409 conflict.
	// Omitted = last-writer-wins (default single-user clients).
	IfMatchUpdatedAt *time.Time `json:"if_match_updated_at"`
}

func (u *UpdateInput) Normalize() {
	if u.URL != nil {
		s := strings.TrimSpace(*u.URL)
		u.URL = &s
	}
	if u.Title != nil {
		s := strings.TrimSpace(*u.Title)
		u.Title = &s
	}
	tags.NormalizeCreateInputs(u.PendingTags)
}

func (u UpdateInput) Validate() error {
	if u.URL != nil {
		if *u.URL == "" {
			return errMsg("url is required")
		}
		parsed, err := url.Parse(*u.URL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errMsg("url must be an absolute http(s) URL")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return errMsg("url scheme must be http or https")
		}
	}
	if u.Title != nil {
		if *u.Title == "" {
			return errMsg("title is required")
		}
		if len(*u.Title) > MaxTitleBytes {
			return errMsg("title too long (max 500)")
		}
	}
	// Slug: explicit value must pass the same check the DB enforces. A
	// `null` payload (SlugSet=true, Slug=nil) means "regenerate from
	// title" — that's handled by the repository, not validated here.
	if u.SlugSet && u.Slug != nil && !SlugIsValid(*u.Slug) {
		return errMsg("slug must match [a-z0-9-]+ (no leading/trailing/consecutive hyphens, not purely numeric, max 80 chars)")
	}
	if u.CheckIntervalSet && u.CheckInterval != nil && !ValidCheckInterval(*u.CheckInterval) {
		return errMsg("check_interval must be one of hourly, daily, weekly")
	}
	if err := tags.ValidateCreateInputs(u.PendingTags); err != nil {
		return errMsg(err.Error())
	}
	return nil
}

type ListQuery = listquery.Params

// UnmarshalJSON for UpdateInput preserves the "null vs absent" distinction
// on `folder_id` and `slug` (both tri-state). For each: presence flips the
// *Set flag true; explicit `null` keeps the value pointer nil with the flag
// true (= clear/regenerate); absence leaves both untouched.
func (u *UpdateInput) UnmarshalJSON(data []byte) error {
	type alias UpdateInput
	aux := struct {
		FolderID      json.RawMessage `json:"folder_id"`
		Slug          json.RawMessage `json:"slug"`
		CheckInterval json.RawMessage `json:"check_interval"`
		*alias
	}{alias: (*alias)(u)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	var err error
	if u.FolderIDSet, u.FolderID, err = jsonopt.DecodeOptionalInt64(aux.FolderID); err != nil {
		return err
	}
	if u.SlugSet, u.Slug, err = jsonopt.DecodeOptionalString(aux.Slug, true); err != nil {
		return err
	}
	if u.CheckIntervalSet, u.CheckInterval, err = jsonopt.DecodeOptionalString(aux.CheckInterval, true); err != nil {
		return err
	}
	return nil
}

type validationErr string

func (e validationErr) Error() string { return string(e) }
func errMsg(s string) error           { return validationErr(s) }

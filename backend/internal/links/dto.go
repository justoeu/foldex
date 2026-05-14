package links

import (
	"encoding/json"
	"net/url"
	"strings"
)

type CreateInput struct {
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	TagIDs      []int64 `json:"tag_ids"`
	Pinned      bool    `json:"pinned"`
	FolderID    *int64  `json:"folder_id"`
}

func (c *CreateInput) Normalize() {
	c.URL = strings.TrimSpace(c.URL)
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" {
		c.Title = c.URL
	}
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
	if len(c.Title) > 500 {
		return errMsg("title too long (max 500)")
	}
	return nil
}

type UpdateInput struct {
	URL         *string  `json:"url"`
	Title       *string  `json:"title"`
	Description *string  `json:"description"`
	TagIDs      *[]int64 `json:"tag_ids"`
	Pinned      *bool    `json:"pinned"`
	// FolderID has 3 states by intent:
	//   field absent in JSON → don't touch
	//   {"folder_id": N}     → assign to folder N
	//   {"folder_id": null}  → clear (no folder)
	// We can't disambiguate "absent" vs "null" with a `*int64` alone, so we
	// carry an explicit FolderIDSet flag that the JSON decoder flips in
	// UnmarshalJSON (see helper below).
	FolderID    *int64 `json:"-"`
	FolderIDSet bool   `json:"-"`
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
		if len(*u.Title) > 500 {
			return errMsg("title too long (max 500)")
		}
	}
	return nil
}

type ListQuery struct {
	Q         string
	TagIDs    []int64
	Sort      string // created|clicks|recent
	Limit     int
	Offset    int
	FolderID  *int64 // ?folder_id=N → links inside folder N
	Ungrouped bool   // ?ungrouped=1 → links with folder_id IS NULL
}

// UnmarshalJSON for UpdateInput preserves the "null vs absent" distinction on
// folder_id: presence flips FolderIDSet=true; null inside payload leaves
// FolderID=nil + FolderIDSet=true (= clear).
func (u *UpdateInput) UnmarshalJSON(data []byte) error {
	type alias UpdateInput
	aux := struct {
		FolderID json.RawMessage `json:"folder_id"`
		*alias
	}{alias: (*alias)(u)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.FolderID) == 0 {
		// field absent
		u.FolderIDSet = false
		u.FolderID = nil
		return nil
	}
	u.FolderIDSet = true
	if string(aux.FolderID) == "null" {
		u.FolderID = nil
		return nil
	}
	var n int64
	if err := json.Unmarshal(aux.FolderID, &n); err != nil {
		return err
	}
	u.FolderID = &n
	return nil
}

type validationErr string

func (e validationErr) Error() string { return string(e) }
func errMsg(s string) error           { return validationErr(s) }

package folders

import (
	"encoding/json"
	"strings"

	"foldex/internal/pkg/cssvalid"
)

type CreateInput struct {
	Name     string `json:"name"`
	Color    string `json:"color"`
	ParentID *int64 `json:"parent_id"`
}

func (c *CreateInput) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.Color = strings.TrimSpace(c.Color)
	if c.Color == "" {
		c.Color = "#6366F1"
	}
}

func (c CreateInput) Validate() error {
	if c.Name == "" {
		return errMsg("name is required")
	}
	if len(c.Name) > 200 {
		return errMsg("name too long (max 200)")
	}
	if !cssvalid.IsValidColor(c.Color) {
		return errMsg("color must be a hex (#abc, #aabbcc) or linear-gradient(135deg, #hex, #hex)")
	}
	return nil
}

type UpdateInput struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
	// Tri-state same pattern as links.UpdateInput.FolderID:
	//   field absent in JSON → don't touch
	//   {"parent_id": N}     → move under folder N
	//   {"parent_id": null}  → promote to root (no parent)
	ParentID    *int64 `json:"-"`
	ParentIDSet bool   `json:"-"`
}

func (u UpdateInput) Empty() bool {
	return u.Name == nil && u.Color == nil && !u.ParentIDSet
}

func (u *UpdateInput) UnmarshalJSON(data []byte) error {
	type alias UpdateInput
	aux := struct {
		ParentID json.RawMessage `json:"parent_id"`
		*alias
	}{alias: (*alias)(u)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.ParentID) == 0 {
		u.ParentIDSet = false
		u.ParentID = nil
		return nil
	}
	u.ParentIDSet = true
	if string(aux.ParentID) == "null" {
		u.ParentID = nil
		return nil
	}
	var n int64
	if err := json.Unmarshal(aux.ParentID, &n); err != nil {
		return err
	}
	u.ParentID = &n
	return nil
}

func (u *UpdateInput) Normalize() {
	if u.Name != nil {
		s := strings.TrimSpace(*u.Name)
		u.Name = &s
	}
	if u.Color != nil {
		s := strings.TrimSpace(*u.Color)
		u.Color = &s
	}
}

func (u UpdateInput) Validate() error {
	if u.Name != nil {
		if *u.Name == "" {
			return errMsg("name is required")
		}
		if len(*u.Name) > 200 {
			return errMsg("name too long (max 200)")
		}
	}
	if u.Color != nil && !cssvalid.IsValidColor(*u.Color) {
		return errMsg("color must be a hex (#abc, #aabbcc) or linear-gradient(135deg, #hex, #hex)")
	}
	return nil
}

type validationErr string

func (e validationErr) Error() string { return string(e) }

func errMsg(s string) error { return validationErr(s) }

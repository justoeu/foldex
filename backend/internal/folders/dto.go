package folders

import (
	"encoding/json"
	"fmt"
	"strings"

	"foldex/internal/pkg/cssvalid"
)

// minPasswordLen is deliberately low — this protects against casual
// glancing, not brute force (see CLAUDE.md's folder-password ADR for the
// threat-model reasoning). It exists only to reject an accidental
// near-empty password, not to enforce a "strong password" policy.
const minPasswordLen = 4

type CreateInput struct {
	Name     string  `json:"name"`
	Color    string  `json:"color"`
	ParentID *int64  `json:"parent_id"`
	Password *string `json:"password"`
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
	if c.Password != nil && len(*c.Password) < minPasswordLen {
		return errMsg(fmt.Sprintf("password must be at least %d characters", minPasswordLen))
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
	// Password is the same tri-state shape:
	//   field absent          → password unchanged
	//   {"password": "x"}     → set/replace the password
	//   {"password": null}    → remove password protection
	// CurrentPassword is a plain (non-tri-state) field, required by the
	// repository whenever PasswordSet is true AND the folder currently has
	// a password — enforced there (not here), since Validate() has no DB
	// access to know the folder's current state.
	Password        *string `json:"-"`
	PasswordSet     bool    `json:"-"`
	CurrentPassword *string `json:"current_password"`
}

func (u UpdateInput) Empty() bool {
	return u.Name == nil && u.Color == nil && !u.ParentIDSet && !u.PasswordSet
}

func (u *UpdateInput) UnmarshalJSON(data []byte) error {
	type alias UpdateInput
	aux := struct {
		ParentID json.RawMessage `json:"parent_id"`
		Password json.RawMessage `json:"password"`
		*alias
	}{alias: (*alias)(u)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.ParentID) == 0 {
		u.ParentIDSet = false
		u.ParentID = nil
	} else {
		u.ParentIDSet = true
		if string(aux.ParentID) == "null" {
			u.ParentID = nil
		} else {
			var n int64
			if err := json.Unmarshal(aux.ParentID, &n); err != nil {
				return err
			}
			u.ParentID = &n
		}
	}
	if len(aux.Password) == 0 {
		u.PasswordSet = false
		u.Password = nil
	} else {
		u.PasswordSet = true
		if string(aux.Password) == "null" {
			u.Password = nil
		} else {
			var s string
			if err := json.Unmarshal(aux.Password, &s); err != nil {
				return err
			}
			u.Password = &s
		}
	}
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
	if u.PasswordSet && u.Password != nil && len(*u.Password) < minPasswordLen {
		return errMsg(fmt.Sprintf("password must be at least %d characters", minPasswordLen))
	}
	return nil
}

type validationErr string

func (e validationErr) Error() string { return string(e) }

func errMsg(s string) error { return validationErr(s) }

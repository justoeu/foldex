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

// maxPasswordHintLen caps the reminder phrase. It's non-secret display text,
// not a password, so a generous-but-bounded ceiling is enough.
const maxPasswordHintLen = 200

type CreateInput struct {
	Name         string  `json:"name"`
	Color        string  `json:"color"`
	ParentID     *int64  `json:"parent_id"`
	Password     *string `json:"password"`
	PasswordHint *string `json:"password_hint"`
}

func (c *CreateInput) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.Color = strings.TrimSpace(c.Color)
	if c.Color == "" {
		c.Color = "#6366F1"
	}
	c.PasswordHint = normalizeHint(c.PasswordHint)
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
	if err := validateHint(c.PasswordHint, c.Password); err != nil {
		return err
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
	// PasswordHint is the same tri-state shape as Password:
	//   field absent            → hint unchanged
	//   {"password_hint": "x"}  → set/replace the hint
	//   {"password_hint": null} → remove the hint
	PasswordHint    *string `json:"-"`
	PasswordHintSet bool    `json:"-"`
}

func (u UpdateInput) Empty() bool {
	return u.Name == nil && u.Color == nil && !u.ParentIDSet && !u.PasswordSet && !u.PasswordHintSet
}

func (u *UpdateInput) UnmarshalJSON(data []byte) error {
	type alias UpdateInput
	aux := struct {
		ParentID     json.RawMessage `json:"parent_id"`
		Password     json.RawMessage `json:"password"`
		PasswordHint json.RawMessage `json:"password_hint"`
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
	if len(aux.PasswordHint) == 0 {
		u.PasswordHintSet = false
		u.PasswordHint = nil
	} else {
		u.PasswordHintSet = true
		if string(aux.PasswordHint) == "null" {
			u.PasswordHint = nil
		} else {
			var s string
			if err := json.Unmarshal(aux.PasswordHint, &s); err != nil {
				return err
			}
			u.PasswordHint = &s
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
	if u.PasswordHintSet {
		u.PasswordHint = normalizeHint(u.PasswordHint)
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
	// hint == password equality is checked in the repository (it needs the
	// folder's effective hash); here we only bound length. When the hint is
	// set alongside a new password, we can also catch equality early.
	if u.PasswordHintSet {
		var pw *string
		if u.PasswordSet {
			pw = u.Password
		}
		if err := validateHint(u.PasswordHint, pw); err != nil {
			return err
		}
	}
	return nil
}

// normalizeHint trims a hint and collapses an empty/blank result to nil so a
// whitespace-only hint is treated as "no hint" rather than stored.
func normalizeHint(hint *string) *string {
	if hint == nil {
		return nil
	}
	s := strings.TrimSpace(*hint)
	if s == "" {
		return nil
	}
	return &s
}

// validateHint bounds the hint length and rejects a hint equal to the plaintext
// password when both are known here (create, or update setting both at once).
// The repository does the authoritative equality check against the stored hash
// for the update-hint-only case. hint is assumed already normalized.
func validateHint(hint, password *string) error {
	if hint == nil {
		return nil
	}
	if len(*hint) > maxPasswordHintLen {
		return errMsg(fmt.Sprintf("password hint too long (max %d)", maxPasswordHintLen))
	}
	if password != nil && strings.EqualFold(strings.TrimSpace(*password), *hint) {
		return errMsg("password hint must not be the same as the password")
	}
	return nil
}

type validationErr string

func (e validationErr) Error() string { return string(e) }

func errMsg(s string) error { return validationErr(s) }

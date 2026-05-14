package tags

import "strings"

type CreateInput struct {
	Name  string  `json:"name"`
	Color string  `json:"color"`
	Icon  *string `json:"icon"`
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
	if len(c.Name) > 80 {
		return errMsg("name too long (max 80)")
	}
	return nil
}

type UpdateInput struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
	Icon  *string `json:"icon"`
}

func (u UpdateInput) Empty() bool {
	return u.Name == nil && u.Color == nil && u.Icon == nil
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
		if len(*u.Name) > 80 {
			return errMsg("name too long (max 80)")
		}
	}
	return nil
}

type validationErr string

func (e validationErr) Error() string { return string(e) }

func errMsg(s string) error { return validationErr(s) }

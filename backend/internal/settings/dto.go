package settings

import (
	"fmt"
	"strings"
)

// minMasterPasswordLen is stricter than the folder-password minimum: the
// master password is the single recovery key for every locked folder, so it
// warrants a slightly higher floor. Still a floor, not a strength policy — the
// strength meter in the UI is guidance, not a hard gate. maxMasterHintLen
// bounds the reminder phrase.
const (
	minMasterPasswordLen = 8
	maxMasterHintLen     = 200
)

// setMasterInput is the PUT /master-password body. CurrentPassword is required
// by the handler only when a master password is already configured (changing
// it), enforced there rather than here since Validate has no DB access. Hint is
// an optional non-secret reminder phrase — normalized to nil when blank, and
// rejected when it equals the password.
type setMasterInput struct {
	Password        string  `json:"password"`
	CurrentPassword *string `json:"current_password"`
	Hint            *string `json:"hint"`
}

// NormalizedHint trims the hint and collapses a blank value to nil.
func (in setMasterInput) NormalizedHint() *string {
	if in.Hint == nil {
		return nil
	}
	s := strings.TrimSpace(*in.Hint)
	if s == "" {
		return nil
	}
	return &s
}

func (in setMasterInput) Validate() error {
	if len(in.Password) < minMasterPasswordLen {
		return fmt.Errorf("master password must be at least %d characters", minMasterPasswordLen)
	}
	if hint := in.NormalizedHint(); hint != nil {
		if len(*hint) > maxMasterHintLen {
			return fmt.Errorf("hint too long (max %d)", maxMasterHintLen)
		}
		if strings.EqualFold(*hint, strings.TrimSpace(in.Password)) {
			return fmt.Errorf("hint must not be the same as the password")
		}
	}
	return nil
}

// clearMasterInput is the DELETE /master-password body. CurrentPassword proves
// the caller knows the master before it is removed.
type clearMasterInput struct {
	CurrentPassword string `json:"current_password"`
}

// statusOutput is the GET /master-password response. It NEVER carries the hash,
// but DOES carry the non-secret hint (nil when unset) so the UI can surface it.
type statusOutput struct {
	Configured bool    `json:"configured"`
	Hint       *string `json:"hint"`
}

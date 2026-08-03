// Package jsonopt decodes tri-state JSON fields (absent / null / value).
package jsonopt

import (
	"encoding/json"
	"strings"
)

// DecodeOptionalInt64 decodes a RawMessage into the tri-state shape used by
// UpdateInput DTOs: absent → (false, nil); null → (true, nil); number → (true, &n).
func DecodeOptionalInt64(raw json.RawMessage) (set bool, val *int64, err error) {
	if len(raw) == 0 {
		return false, nil, nil
	}
	if string(raw) == "null" {
		return true, nil, nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return false, nil, err
	}
	return true, &n, nil
}

// DecodeOptionalString decodes a RawMessage into tri-state string fields.
// Empty string after TrimSpace is treated as null (clear) when trim is true.
func DecodeOptionalString(raw json.RawMessage, trim bool) (set bool, val *string, err error) {
	if len(raw) == 0 {
		return false, nil, nil
	}
	if string(raw) == "null" {
		return true, nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false, nil, err
	}
	if trim {
		s = strings.TrimSpace(s)
	}
	if s == "" {
		return true, nil, nil
	}
	return true, &s, nil
}

// DecodeOptionalStringRaw is like DecodeOptionalString but never trims and
// never collapses "" to nil (used for password fields that must preserve empty).
func DecodeOptionalStringRaw(raw json.RawMessage) (set bool, val *string, err error) {
	if len(raw) == 0 {
		return false, nil, nil
	}
	if string(raw) == "null" {
		return true, nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false, nil, err
	}
	return true, &s, nil
}

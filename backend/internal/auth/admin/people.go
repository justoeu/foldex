package admin

import (
	"strconv"

	"foldex/internal/pkg/authctx"
)

func AssignableRole(role authctx.Role) bool {
	return role.Valid() && role != authctx.RoleOwner
}

func ValidStatus(status string) bool {
	return status == "active" || status == "disabled"
}

func ParseAfterID(raw string) int64 {
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func IsSelf(caller, target authctx.UserID) bool {
	return caller == target
}

package secrethint

import "strings"

func EqualsPassword(hint, password string) bool {
	h := strings.TrimSpace(hint)
	p := strings.TrimSpace(password)
	return h != "" && p != "" && strings.EqualFold(h, p)
}

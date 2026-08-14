package notemedia

import (
	"regexp"
	"sort"
	"strings"
)

var proxyKeyRE = regexp.MustCompile(`(?:^|["'\s(=])/api/files/(notes/[A-Za-z0-9._-]+)`)

// Keys returns unique local notes/ object keys referenced by the supplied
// HTML/URL values. A caller must still prove ownership in note_media before a
// key can become a durable ref or authorize deletion.
func Keys(values ...string) []string {
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, match := range proxyKeyRE.FindAllStringSubmatch(value, -1) {
			if len(match) == 2 {
				seen[match[1]] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Rewrite replaces only local note-media keys present in mapping. It leaves
// external URLs and legacy/unmapped public references unchanged.
func Rewrite(value string, mapping map[string]string) string {
	return proxyKeyRE.ReplaceAllStringFunc(value, func(match string) string {
		const marker = "/api/files/"
		idx := strings.LastIndex(match, marker)
		if idx < 0 {
			return match
		}
		oldKey := match[idx+len(marker):]
		if newKey, ok := mapping[oldKey]; ok {
			return match[:idx+len(marker)] + newKey
		}
		return match
	})
}

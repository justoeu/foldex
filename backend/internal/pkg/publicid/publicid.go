package publicid

import "strconv"

// Parse returns a positive int64 only when raw contains decimal digits alone.
func Parse(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	for i := range len(raw) {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// Package clampint provides a single numeric-knob clamping helper used by
// every handler that accepts a user-controlled query-string integer. Without
// clamping, ?days=2147483647 or ?limit=999999999 feed the planner unbounded
// work — auth-gated DoS otherwise. Formerly copy-pasted in internal/links and
// internal/stats.
package clampint

import "strconv"

// Int parses a query-string integer and clamps it to [lo, hi]. Returns def on
// empty/invalid input. All numeric query knobs across links/stats/importer
// handlers use this single implementation.
func Int(raw string, def, lo, hi int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

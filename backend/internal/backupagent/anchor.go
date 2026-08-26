package backupagent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Anchor is a wall-clock schedule: "03:30" fires daily at 03:30, "04:30 sun"
// fires weekly on Sunday. The repo's *_SEC/*_MIN convention encodes DURATIONS;
// an anchor is an instant of wall time, and forcing it into minutes-since-
// midnight would be hostile to the operator — this is the one documented
// deviation (SDD-OPS-BACKUP §6).
type Anchor struct {
	Hour, Minute int
	Weekday      time.Weekday // meaningful only when Weekly
	Weekly       bool
	set          bool
}

// Enabled reports whether the anchor was configured at all.
func (a Anchor) Enabled() bool { return a.set }

// Interval is the expected gap between firings, used by catch-up and by the
// staleness contract the UI and the alert rules render.
func (a Anchor) Interval() time.Duration {
	if a.Weekly {
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// ParseAnchor accepts "HH:MM" or "HH:MM <weekday>" (sun..sat, case-insensitive).
func ParseAnchor(raw string) (Anchor, error) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 || len(fields) > 2 {
		return Anchor{}, fmt.Errorf("want \"HH:MM\" or \"HH:MM sun\", got %q", raw)
	}
	hm := strings.Split(fields[0], ":")
	if len(hm) != 2 {
		return Anchor{}, fmt.Errorf("want \"HH:MM\", got %q", fields[0])
	}
	hour, errH := strconv.Atoi(hm[0])
	minute, errM := strconv.Atoi(hm[1])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return Anchor{}, fmt.Errorf("%q is not a valid 24h wall time", fields[0])
	}
	a := Anchor{Hour: hour, Minute: minute, set: true}
	if len(fields) == 2 {
		wd, ok := weekdays[strings.ToLower(fields[1])]
		if !ok {
			return Anchor{}, fmt.Errorf("%q is not a weekday (sun..sat)", fields[1])
		}
		a.Weekday, a.Weekly = wd, true
	}
	return a, nil
}

// Next returns the first firing instant strictly after now, in now's location.
// It is recomputed after every firing rather than derived by adding fixed
// intervals — that is what absorbs DST: on a transition day an anchor may fire
// twice or be skipped, which is documented and accepted (catch-up covers the
// skip, and a duplicate run is harmless — the second is fast and retention
// prunes it).
func (a Anchor) Next(now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), a.Hour, a.Minute, 0, 0, now.Location())
	if a.Weekly {
		for next.Weekday() != a.Weekday || !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next
	}
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Due reports whether a job whose last success was lastSuccess should run now
// as boot catch-up: never succeeded, or one full interval plus grace behind.
// The 25% grace keeps a restart minutes after the anchor from double-running a
// job that in fact succeeded on time.
func (a Anchor) Due(now, lastSuccess time.Time) bool {
	if !a.set {
		return false
	}
	if lastSuccess.IsZero() {
		return true
	}
	grace := a.Interval() / 4
	return now.Sub(lastSuccess) > a.Interval()+grace
}

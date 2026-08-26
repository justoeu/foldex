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

// PreviousSlot returns the anchor occurrence at or immediately before now —
// the slot a boot catch-up run satisfies. scheduled_for's contract (migration
// 000040, SDD §3) is "the slot this run satisfies", so catch-up must record
// the MISSED anchor instant, not the moment the agent happened to restart.
// Derived as Next minus one interval: around a DST transition the subtraction
// can shift the wall time by the offset change — the same accepted behaviour
// Next documents, now in one place instead of inlined in the scheduler.
func (a Anchor) PreviousSlot(now time.Time) time.Time {
	return a.Next(now).Add(-a.Interval())
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

// intervalDue is the boot catch-up decision for interval-scheduled jobs
// (Anchor.Due's sibling): run now when the job never succeeded, or when the
// last success is more than one interval plus 25% grace behind. The grace
// keeps a restart minutes after a tick from double-running a job that in fact
// succeeded on time.
func intervalDue(now, lastSuccess time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	if lastSuccess.IsZero() {
		return true
	}
	return now.Sub(lastSuccess) > interval+interval/4
}

// String renders the anchor back in its env-var syntax, for logs.
func (a Anchor) String() string {
	if !a.set {
		return "disabled"
	}
	s := fmt.Sprintf("%02d:%02d", a.Hour, a.Minute)
	if a.Weekly {
		s += " " + strings.ToLower(a.Weekday.String()[:3])
	}
	return s
}

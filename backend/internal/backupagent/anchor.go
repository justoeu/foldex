package backupagent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Anchor is the parser for the BACKUP_*_AT env syntax and nothing else:
// "03:30" is a daily wall time, "04:30 sun" pins it to a weekday. The repo's
// *_SEC/*_MIN convention encodes DURATIONS; an anchor is an instant of wall
// time, and forcing it into minutes-since-midnight would be hostile to the
// operator — this is the one documented deviation (SDD-OPS-BACKUP §6).
//
// The scheduling math lives on Timing. A bare time cannot answer "when do you
// fire next" on its own: which DAYS it fires on is a property of the agenda
// (a set of weekdays), not of the time, and an Anchor that answered anyway
// would have to guess the missing half.
type Anchor struct {
	Hour, Minute int
	Weekday      time.Weekday // meaningful only when Weekly
	Weekly       bool
	set          bool
}

// Enabled reports whether the anchor was configured at all.
func (a Anchor) Enabled() bool { return a.set }

// weekdayNames is the weekday vocabulary, indexed by weekday — the ONE place
// sun..sat is spelled out. Anything that renders a day reads it here.
var weekdayNames = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// weekdays is the parsing direction, DERIVED from the rendering one rather
// than spelled a second time. Two independent literals could drift into a
// vocabulary that accepts a name it cannot render back, and the drift would
// show up as an agenda the admin form saved and the heartbeat then reported
// differently — with nothing failing in between.
var weekdays = func() map[string]time.Weekday {
	m := make(map[string]time.Weekday, len(weekdayNames))
	for wd := time.Sunday; wd <= time.Saturday; wd++ {
		m[weekdayNames[wd]] = wd
	}
	return m
}()

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

// String renders the anchor back in its env-var syntax, for logs.
func (a Anchor) String() string {
	if !a.set {
		return "disabled"
	}
	s := fmt.Sprintf("%02d:%02d", a.Hour, a.Minute)
	if a.Weekly {
		s += " " + weekdayNames[a.Weekday]
	}
	return s
}

// timeOnly strips the weekday off an env anchor: inside a Timing the days are
// the Timing's, and an anchor that kept its own would render "01:00 sun · sun".
func timeOnly(a Anchor) Anchor {
	return Anchor{Hour: a.Hour, Minute: a.Minute, set: a.set}
}

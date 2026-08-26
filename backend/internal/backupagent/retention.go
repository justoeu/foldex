package backupagent

import (
	"path"
	"sort"
	"strings"
	"time"
)

// dumpKeyPrefix is the object-key namespace for instance dumps. The
// YYYY/MM/DD layout is load-bearing: retention classifies keys by parsing the
// date back out, so pruning works from a plain LIST with no extra state.
const dumpKeyPrefix = "backups/dump/"

// dumpKey builds the object key for a dump taken at ts. The extension says
// what the bytes are: an operator staring at the bucket must know whether
// `age -d` comes first.
func dumpKey(ts time.Time, encrypted bool) string {
	name := "foldex-" + ts.UTC().Format("20060102-150405") + ".dump"
	if encrypted {
		name += ".age"
	}
	return dumpKeyPrefix + ts.UTC().Format("2006/01/02") + "/" + name
}

// dumpKeyDate parses the date a dump key was taken on, ok=false for foreign
// keys in the namespace (never delete what we cannot classify).
func dumpKeyDate(key string) (time.Time, bool) {
	rest, found := strings.CutPrefix(key, dumpKeyPrefix)
	if !found {
		return time.Time{}, false
	}
	dir := path.Dir(rest) // "2006/01/02"
	t, err := time.Parse("2006/01/02", dir)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// GFSPolicy is the grandfather-father-son retention configuration.
type GFSPolicy struct {
	Daily   int // most recent N distinct days
	Weekly  int // most recent N Sundays
	Monthly int // most recent N first-of-months
}

// keep decides which of keys survive pruning. Classification is by the DATE
// in the key, not object age: a daily that is also a Sunday counts for both
// ladders, exactly like hand-rotated dump directories. Keys that fail to
// parse are always kept — a foreign object in the namespace is a surprise,
// never a deletion target.
func (p GFSPolicy) keep(keys []string) map[string]bool {
	type dated struct {
		key string
		at  time.Time
	}
	kept := make(map[string]bool, len(keys))
	newestPerDay := map[string]dated{}
	for _, k := range keys {
		at, ok := dumpKeyDate(k)
		if !ok {
			kept[k] = true
			continue
		}
		d := dated{key: k, at: at}
		day := at.Format("2006-01-02")
		// Several dumps on one day (manual runs, catch-up): the ladder slots
		// are per-day, and the newest key of the day represents it.
		if cur, exists := newestPerDay[day]; !exists || d.key > cur.key {
			newestPerDay[day] = d
		}
	}
	days := make([]dated, 0, len(newestPerDay))
	for _, d := range newestPerDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].at.After(days[j].at) })

	daily, weekly, monthly := 0, 0, 0
	for _, d := range days {
		hold := false
		if daily < p.Daily {
			daily++
			hold = true
		}
		if d.at.Weekday() == time.Sunday && weekly < p.Weekly {
			weekly++
			hold = true
		}
		if d.at.Day() == 1 && monthly < p.Monthly {
			monthly++
			hold = true
		}
		if hold {
			kept[d.key] = true
		}
	}
	// Same-day extras (older keys of an already-kept day) stay prunable; the
	// dump that just landed is safe because its day is the newest and the
	// daily ladder always claims it first.
	return kept
}

// prunable returns the keys keep() rejected, the actual deletion list.
func (p GFSPolicy) prunable(keys []string) []string {
	kept := p.keep(keys)
	var out []string
	for _, k := range keys {
		if !kept[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

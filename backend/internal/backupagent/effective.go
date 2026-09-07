package backupagent

import (
	"time"

	"foldex/internal/backupjobs"
)

// envTiming is the env-baseline Timing for one job. A weekly env anchor
// becomes a one-day weekday set: the days live on the Timing now.
func envTiming(job string, cfg Config) backupjobs.Timing {
	t := backupjobs.Timing{Source: "env"}
	var anchor backupjobs.Anchor
	switch job {
	case backupjobs.JobDump:
		anchor = cfg.DumpAt
	case backupjobs.JobDrill:
		anchor = cfg.DrillAt
	case backupjobs.JobUserZip:
		anchor = cfg.UserZipAt
	case backupjobs.JobMirror:
		if cfg.MirrorEnabled() {
			t.Interval = cfg.MirrorInterval()
		}
		return t
	default:
		return t
	}
	if !anchor.Enabled() {
		return t
	}
	t.Anchors = []backupjobs.Anchor{backupjobs.TimeOnly(anchor)}
	if anchor.Weekly {
		t.Weekdays = []time.Weekday{anchor.Weekday}
	}
	return t
}

// EffectiveTiming merges the env baseline with a database row for one job. A
// nil or invalid row means the baseline; an invalid row is the caller's to
// log — this function only refuses to honour it. The mirror keeps its
// capability from env: with the mirror off in env there is no source client
// in the process, so a row cannot switch it on and a row only tunes a mirror
// that exists.
func EffectiveTiming(job string, cfg Config, row *backupjobs.JobConfig) backupjobs.Timing {
	env := envTiming(job, cfg)
	if row == nil || backupjobs.ValidateJobConfig(job, *row) != nil {
		return env
	}
	if job == backupjobs.JobMirror && !cfg.MirrorEnabled() {
		return env
	}
	return backupjobs.TimingFromConfig(*row)
}

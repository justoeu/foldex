package backupagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The configurable agenda (ADR-44, SDD-OPS-BACKUP §5.9). The environment
// decides WHICH jobs exist — credentials and the age identity live only
// there, and a database row cannot conjure a secret into this process. The
// database decides WHEN the existing jobs run, because "when" is an operating
// decision the owner takes from the admin UI. The floors below are what keep
// any row — the owner's, or one written by hand in SQL — from lowering
// protection under the env baseline: an absent or invalid row falls back to
// the env schedule, and only user_zip may be switched off by a row at all.
const (
	// MinDumpTimes..MaxDumpTimes bound how many daily dumps a row may ask
	// for. The lower bound is the floor that matters: a dump row can trim the
	// agenda to once a day, never to zero.
	MinDumpTimes = 1
	MaxDumpTimes = 6
	// Mirror cadence bounds, minutes. The lower bound keeps a row from
	// turning the mirror into a hot loop against the origin; the upper keeps
	// it from stretching past daily — below the baseline the alert rules and
	// the staleness contract assume.
	MinMirrorIntervalMin = 15
	MaxMirrorIntervalMin = 1440
)

// JobConfig is the per-job document stored in backup_schedule.config. One
// struct for the four shapes rather than four types: the column is a single
// jsonb and the shape is discriminated by the job name, exactly like
// backup_run.meta.
type JobConfig struct {
	// Times: dump only — daily wall times ("HH:MM"), 1..6 entries.
	Times []string `json:"times,omitempty"`
	// Time: drill and user_zip — one daily/weekly wall time.
	Time string `json:"time,omitempty"`
	// Weekday: drill only — sun..sat. The drill stays weekly by design; the
	// row chooses WHICH week slot, never a lower frequency.
	Weekday string `json:"weekday,omitempty"`
	// IntervalMin: mirror only.
	IntervalMin int `json:"interval_min,omitempty"`
	// Enabled: user_zip only — the one job a row may disable, because it is a
	// product convenience, not the instance's protection. Pointer so an
	// absent field can be told apart from an explicit false.
	Enabled *bool `json:"enabled,omitempty"`
}

// ValidateJobConfig enforces the compiled floors for one job's row. Both
// writers-side (the backend's PUT) and reader-side (the agent's load) call
// it, so a row that skipped the API degrades to the env baseline instead of
// being half-honoured.
func ValidateJobConfig(job string, cfg JobConfig) error {
	switch job {
	case JobDump:
		if cfg.Time != "" || cfg.Weekday != "" || cfg.IntervalMin != 0 || cfg.Enabled != nil {
			return fmt.Errorf("dump schedule accepts only \"times\"")
		}
		if len(cfg.Times) < MinDumpTimes || len(cfg.Times) > MaxDumpTimes {
			return fmt.Errorf("dump needs between %d and %d daily times — the floor is one dump per day, never zero", MinDumpTimes, MaxDumpTimes)
		}
		seen := map[string]bool{}
		for _, t := range cfg.Times {
			a, err := ParseAnchor(t)
			if err != nil {
				return fmt.Errorf("dump time %q: %w", t, err)
			}
			if a.Weekly {
				return fmt.Errorf("dump time %q: weekday not allowed — dump times are daily", t)
			}
			if seen[a.String()] {
				return fmt.Errorf("dump time %q repeats", t)
			}
			seen[a.String()] = true
		}
	case JobDrill:
		if cfg.Times != nil || cfg.IntervalMin != 0 || cfg.Enabled != nil {
			return fmt.Errorf("drill schedule accepts only \"time\" and \"weekday\"")
		}
		if cfg.Time == "" || cfg.Weekday == "" {
			return fmt.Errorf("drill needs \"time\" and \"weekday\" — it is weekly by design and a row cannot switch it off")
		}
		if _, err := ParseAnchor(cfg.Time + " " + cfg.Weekday); err != nil {
			return fmt.Errorf("drill schedule: %w", err)
		}
	case JobMirror:
		if cfg.Times != nil || cfg.Time != "" || cfg.Weekday != "" || cfg.Enabled != nil {
			return fmt.Errorf("mirror schedule accepts only \"interval_min\"")
		}
		if cfg.IntervalMin < MinMirrorIntervalMin || cfg.IntervalMin > MaxMirrorIntervalMin {
			return fmt.Errorf("mirror interval must be between %d and %d minutes — a row tunes the cadence, it cannot switch the mirror off", MinMirrorIntervalMin, MaxMirrorIntervalMin)
		}
	case JobUserZip:
		if cfg.Times != nil || cfg.Weekday != "" || cfg.IntervalMin != 0 {
			return fmt.Errorf("user_zip schedule accepts only \"enabled\" and \"time\"")
		}
		if cfg.Enabled == nil {
			return fmt.Errorf("user_zip needs \"enabled\"")
		}
		if *cfg.Enabled {
			if cfg.Time == "" {
				return fmt.Errorf("user_zip needs \"time\" while enabled")
			}
			a, err := ParseAnchor(cfg.Time)
			if err != nil {
				return fmt.Errorf("user_zip time: %w", err)
			}
			if a.Weekly {
				return fmt.Errorf("user_zip time %q: weekday not allowed — the archive is daily", cfg.Time)
			}
		}
	default:
		return fmt.Errorf("unknown job %q", job)
	}
	return nil
}

// Timing is one job's effective runtime schedule after merging the env
// baseline with the database row: either wall-clock anchors or an interval,
// with Source recording which side won so the heartbeat — and through it the
// UI — can say where the agenda came from.
type Timing struct {
	Anchors  []Anchor
	Interval time.Duration
	Source   string // "db" | "env"
}

// Enabled reports whether the timing schedules anything at all.
func (t Timing) Enabled() bool { return len(t.Anchors) > 0 || t.Interval > 0 }

// Next returns the earliest firing instant strictly after now across the
// anchors, plus the anchor that owns it — the dump may carry several daily
// times, and the scheduler sleeps until the nearest one.
func (t Timing) Next(now time.Time) (time.Time, Anchor) {
	var best time.Time
	var owner Anchor
	for _, a := range t.Anchors {
		n := a.Next(now)
		if best.IsZero() || n.Before(best) {
			best, owner = n, a
		}
	}
	return best, owner
}

// MaxGap is the longest legitimate silence between consecutive firings — the
// interval the catch-up decision and the staleness contract compare against.
// With one anchor it is the anchor's own interval; with several daily times
// it is the widest wraparound gap between them.
func (t Timing) MaxGap() time.Duration {
	if t.Interval > 0 {
		return t.Interval
	}
	if len(t.Anchors) == 0 {
		return 0
	}
	if len(t.Anchors) == 1 {
		return t.Anchors[0].Interval()
	}
	// The wraparound arithmetic below assumes DAILY anchors — today only the
	// dump carries several, and its validation refuses weekdays. If a weekly
	// anchor ever lands in a multi-anchor timing, minutes-since-midnight
	// would understate its week-long gap and every boot would catch up
	// spuriously; the widest single interval is the honest answer there.
	for _, a := range t.Anchors {
		if a.Weekly {
			widest := t.Anchors[0].Interval()
			for _, a := range t.Anchors[1:] {
				if a.Interval() > widest {
					widest = a.Interval()
				}
			}
			return widest
		}
	}
	mins := make([]int, 0, len(t.Anchors))
	for _, a := range t.Anchors {
		mins = append(mins, a.Hour*60+a.Minute)
	}
	sort.Ints(mins)
	widest := mins[0] + 24*60 - mins[len(mins)-1]
	for i := 1; i < len(mins); i++ {
		if gap := mins[i] - mins[i-1]; gap > widest {
			widest = gap
		}
	}
	return time.Duration(widest) * time.Minute
}

// Due is the boot catch-up decision across the anchors, MaxGap-based so a
// twice-daily dump restarting after a missed evening slot still catches up.
func (t Timing) Due(now, lastSuccess time.Time) bool {
	if !t.Enabled() || t.Interval > 0 {
		return false
	}
	if lastSuccess.IsZero() {
		return true
	}
	gap := t.MaxGap()
	return now.Sub(lastSuccess) > gap+gap/4
}

// PreviousSlot is the most recent anchor occurrence at or before now — the
// slot a catch-up run satisfies (scheduled_for's contract, migration 000040).
func (t Timing) PreviousSlot(now time.Time) time.Time {
	var best time.Time
	for _, a := range t.Anchors {
		p := a.PreviousSlot(now)
		if p.After(best) {
			best = p
		}
	}
	return best
}

// String renders the timing for logs and for the heartbeat's schedule field.
func (t Timing) String() string {
	if t.Interval > 0 {
		return fmt.Sprintf("every %dm", int(t.Interval.Minutes()))
	}
	if len(t.Anchors) == 0 {
		return "disabled"
	}
	parts := make([]string, 0, len(t.Anchors))
	for _, a := range t.Anchors {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}

// timingFromConfig turns a validated row into a Timing. Only reached after
// ValidateJobConfig, so parse errors here are impossible by construction —
// they still surface (as a disabled timing) rather than panic.
func timingFromConfig(job string, cfg JobConfig) Timing {
	t := Timing{Source: "db"}
	switch job {
	case JobDump:
		for _, raw := range cfg.Times {
			if a, err := ParseAnchor(raw); err == nil {
				t.Anchors = append(t.Anchors, a)
			}
		}
	case JobDrill:
		if a, err := ParseAnchor(cfg.Time + " " + cfg.Weekday); err == nil {
			t.Anchors = []Anchor{a}
		}
	case JobMirror:
		t.Interval = time.Duration(cfg.IntervalMin) * time.Minute
	case JobUserZip:
		if cfg.Enabled != nil && *cfg.Enabled {
			if a, err := ParseAnchor(cfg.Time); err == nil {
				t.Anchors = []Anchor{a}
			}
		}
	}
	return t
}

// envTiming is the env-baseline Timing for one job.
func envTiming(job string, cfg Config) Timing {
	t := Timing{Source: "env"}
	switch job {
	case JobDump:
		if cfg.DumpAt.Enabled() {
			t.Anchors = []Anchor{cfg.DumpAt}
		}
	case JobDrill:
		if cfg.DrillAt.Enabled() {
			t.Anchors = []Anchor{cfg.DrillAt}
		}
	case JobMirror:
		if cfg.MirrorEnabled() {
			t.Interval = cfg.MirrorInterval()
		}
	case JobUserZip:
		if cfg.UserZipAt.Enabled() {
			t.Anchors = []Anchor{cfg.UserZipAt}
		}
	}
	return t
}

// EffectiveTiming merges the env baseline with a database row for one job. A
// nil or invalid row means the baseline; an invalid row is the caller's to
// log — this function only refuses to honour it. The mirror keeps its
// capability from env: with the mirror off in env there is no source client
// in the process, so a row cannot switch it on and IntervalMin only tunes a
// mirror that exists.
func EffectiveTiming(job string, cfg Config, row *JobConfig) Timing {
	env := envTiming(job, cfg)
	if row == nil || ValidateJobConfig(job, *row) != nil {
		return env
	}
	if job == JobMirror && !cfg.MirrorEnabled() {
		return env
	}
	return timingFromConfig(job, *row)
}

// ScheduleStore reads and writes backup_schedule and the agent heartbeat.
// The agent loads rows and upserts the heartbeat; the backend (via
// backupstatus) writes rows and reads the heartbeat — same table, one
// validation, opposite directions.
type ScheduleStore struct {
	pool *pgxpool.Pool
}

func NewScheduleStore(pool *pgxpool.Pool) *ScheduleStore {
	return &ScheduleStore{pool: pool}
}

// ScheduleRow is one backup_schedule row as the admin surface serves it.
type ScheduleRow struct {
	Job       string    `json:"job"`
	Config    JobConfig `json:"config"`
	UpdatedAt time.Time `json:"updated_at"`
	// UpdatedByEmail resolves through app_user for the band; null once the
	// account is gone (the audit trail keeps the durable record, INV-047).
	UpdatedByEmail *string `json:"updated_by_email"`
}

// Load returns the stored rows by job. A row whose document no longer
// validates is returned anyway — EffectiveTiming refuses it and the caller
// logs; hiding it here would make the fallback invisible.
func (s *ScheduleStore) Load(ctx context.Context) (map[string]ScheduleRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT bs.job, bs.config, bs.updated_at, u.email
		FROM backup_schedule bs
		LEFT JOIN app_user u ON u.id = bs.updated_by`)
	if err != nil {
		return nil, fmt.Errorf("backupagent: load schedule: %w", err)
	}
	defer rows.Close()
	out := map[string]ScheduleRow{}
	for rows.Next() {
		var r ScheduleRow
		var raw []byte
		if err := rows.Scan(&r.Job, &raw, &r.UpdatedAt, &r.UpdatedByEmail); err != nil {
			return nil, fmt.Errorf("backupagent: scan schedule: %w", err)
		}
		if err := json.Unmarshal(raw, &r.Config); err != nil {
			return nil, fmt.Errorf("backupagent: schedule config for %s: %w", r.Job, err)
		}
		out[r.Job] = r
	}
	return out, rows.Err()
}

// Upsert stores one job's row. updatedBy is the acting user's id; 0 records
// NULL (the agent never writes rows — this is the backend's path).
func (s *ScheduleStore) Upsert(ctx context.Context, job string, cfg JobConfig, updatedBy int64) error {
	if err := ValidateJobConfig(job, cfg); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	var by *int64
	if updatedBy != 0 {
		by = &updatedBy
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO backup_schedule (job, config, updated_at, updated_by)
		VALUES ($1, $2, now(), $3)
		ON CONFLICT (job) DO UPDATE
		SET config = EXCLUDED.config, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		job, raw, by)
	if err != nil {
		return fmt.Errorf("backupagent: upsert schedule: %w", err)
	}
	return nil
}

// Delete removes one job's row — the job falls back to its env baseline.
func (s *ScheduleStore) Delete(ctx context.Context, job string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM backup_schedule WHERE job = $1`, job)
	if err != nil {
		return fmt.Errorf("backupagent: delete schedule: %w", err)
	}
	return nil
}

// JobReport is what the heartbeat says about one job: whether this agent can
// run it at all, why not, and the agenda it is actually following. The UI
// renders THIS as the truth — the rows are the editable layer, the report is
// what the process will really do.
type JobReport struct {
	Capable bool `json:"capable"`
	// Reason is set only when not capable: "no_identity" (drill without the
	// age identity), "mirror_off" (no source client in this process),
	// "no_source_credentials" (user_zip without RUSTFS_*).
	Reason   string `json:"reason,omitempty"`
	Source   string `json:"source"`   // "db" | "env"
	Schedule string `json:"schedule"` // Timing.String(), for display
}

// AgentState is the heartbeat row.
type AgentState struct {
	SeenAt  time.Time            `json:"seen_at"`
	Version string               `json:"version"`
	Jobs    map[string]JobReport `json:"jobs"`
}

// Heartbeat upserts the single agent-state row.
func (s *ScheduleStore) Heartbeat(ctx context.Context, state AgentState) error {
	caps, err := json.Marshal(map[string]any{"version": state.Version, "jobs": state.Jobs})
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO backup_agent_state (id, seen_at, capabilities)
		VALUES (1, $1, $2)
		ON CONFLICT (id) DO UPDATE SET seen_at = EXCLUDED.seen_at, capabilities = EXCLUDED.capabilities`,
		state.SeenAt, caps)
	if err != nil {
		return fmt.Errorf("backupagent: heartbeat: %w", err)
	}
	return nil
}

// AgentSeen reads the heartbeat back; ok=false when no agent ever wrote one.
func (s *ScheduleStore) AgentSeen(ctx context.Context) (AgentState, bool, error) {
	var seenAt time.Time
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT seen_at, capabilities FROM backup_agent_state WHERE id = 1`).
		Scan(&seenAt, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentState{}, false, nil
	}
	if err != nil {
		return AgentState{}, false, fmt.Errorf("backupagent: agent state: %w", err)
	}
	var doc struct {
		Version string               `json:"version"`
		Jobs    map[string]JobReport `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return AgentState{}, false, fmt.Errorf("backupagent: agent capabilities: %w", err)
	}
	return AgentState{SeenAt: seenAt, Version: doc.Version, Jobs: doc.Jobs}, true, nil
}

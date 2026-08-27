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
	// MinTimes..MaxTimes bound how many wall times one agenda may carry. The
	// lower bound is the floor that matters: a row can trim the agenda to
	// once a day, never to zero.
	MinTimes = 1
	MaxTimes = 6
	// MinWeekdays is the general floor: an agenda that fires on no day at all
	// is a job switched off through the back door.
	MinWeekdays = 1
	// MinDumpWeekdays is deliberately HIGHER than every other job's floor.
	// The dump is the instance's disaster floor, not a product convenience:
	// four days a week means three consecutive days with no dump at all, and
	// no other job's agenda buys that back.
	MinDumpWeekdays = 5
	// Interval bounds, minutes. The lower bound keeps a row from turning a
	// job into a hot loop against its source; the upper keeps it from
	// stretching past daily — below the baseline the alert rules and the
	// staleness contract assume.
	MinIntervalMin = 15
	MaxIntervalMin = 1440
)

// Scheduling modes. The mode is EXPLICIT, never inferred from which fields a
// document happens to carry: a row with both times and interval_min would
// otherwise be half-honoured in silence.
const (
	modeTimes    = "times"
	modeInterval = "interval"
)

// JobConfig is the per-job document stored in backup_schedule.config. One
// shape for all four jobs: what differs between them is the floors, not the
// vocabulary.
type JobConfig struct {
	// Enabled: only user_zip may be false — it is a product convenience, not
	// the instance's protection. Pointer so an absent field can be told apart
	// from an explicit false.
	Enabled *bool `json:"enabled,omitempty"`
	// Mode: "times" or "interval".
	Mode string `json:"mode"`
	// Times: mode "times" — wall times "HH:MM", MinTimes..MaxTimes, no repeats.
	Times []string `json:"times,omitempty"`
	// Weekdays: mode "times" — a non-empty subset of sun..sat, no repeats.
	Weekdays []string `json:"weekdays,omitempty"`
	// IntervalMin: mode "interval" — MinIntervalMin..MaxIntervalMin.
	IntervalMin int `json:"interval_min,omitempty"`

	// Time and Weekday are the pre-unification vocabulary, kept only so a
	// stored row written by an older backend can still be READ and
	// normalized. They are relics: ValidateJobConfig refuses a document that
	// carries them, because a PUT sending them is a client that did not
	// upgrade, and honouring it would keep the old shape alive by accident.
	Time    string `json:"time,omitempty"`
	Weekday string `json:"weekday,omitempty"`
}

// jobFloor is what one job's agenda may not go under.
type jobFloor struct {
	mayDisable  bool
	minWeekdays int
}

var jobFloors = map[string]jobFloor{
	JobDump:    {mayDisable: false, minWeekdays: MinDumpWeekdays},
	JobDrill:   {mayDisable: false, minWeekdays: MinWeekdays},
	JobMirror:  {mayDisable: false, minWeekdays: MinWeekdays},
	JobUserZip: {mayDisable: true, minWeekdays: MinWeekdays},
}

// ValidateJobConfig enforces the compiled floors for one job's row. Both
// writers-side (the backend's PUT) and reader-side (the agent's load) call
// it, so a row that skipped the API degrades to the env baseline instead of
// being half-honoured. Every refusal names the real numbers: the handler
// returns the message verbatim and the UI renders it without restating it
// (INV-169's reasoning).
func ValidateJobConfig(job string, cfg JobConfig) error {
	floor, known := jobFloors[job]
	if !known {
		return fmt.Errorf("unknown job %q", job)
	}
	if cfg.Time != "" || cfg.Weekday != "" {
		return fmt.Errorf("%q and %q are the previous schedule vocabulary and are read-only — send {\"mode\":\"times\",\"times\":[…],\"weekdays\":[…]}", "time", "weekday")
	}
	if cfg.Mode != modeTimes && cfg.Mode != modeInterval {
		return fmt.Errorf("%s needs \"mode\": %q or %q", job, modeTimes, modeInterval)
	}
	if cfg.Enabled != nil && !floor.mayDisable {
		return fmt.Errorf("%s cannot be switched off — only user_zip carries \"enabled\", because it is the one job that is a product convenience rather than the instance's protection", job)
	}
	if cfg.Enabled != nil && !*cfg.Enabled {
		// A disabled job needs no agenda, and must not carry one: a stored
		// agenda beside enabled:false is two answers to the same question.
		if carriesAgendaDays(cfg) || cfg.IntervalMin != 0 {
			return fmt.Errorf("a disabled %s carries no agenda — send \"enabled\": false alone", job)
		}
		return nil
	}

	switch cfg.Mode {
	case modeTimes:
		if cfg.IntervalMin != 0 {
			return fmt.Errorf("mode %q does not carry \"interval_min\"", modeTimes)
		}
		if err := validateTimes(job, cfg.Times); err != nil {
			return err
		}
		return validateWeekdays(job, cfg.Weekdays, floor.minWeekdays)
	default:
		if carriesAgendaDays(cfg) {
			return fmt.Errorf("mode %q does not carry \"times\" or \"weekdays\"", modeInterval)
		}
		if cfg.IntervalMin < MinIntervalMin || cfg.IntervalMin > MaxIntervalMin {
			return fmt.Errorf("%s interval must be between %d and %d minutes — a row tunes the cadence, it cannot switch the job off", job, MinIntervalMin, MaxIntervalMin)
		}
	}
	return nil
}

// carriesAgendaDays answers "does this document state wall times or weekdays
// at all?" — the one question both absence gates above ask, and the reason
// they ask it through the same function. They spelled it differently once
// (one counted length, the other nil-ness), so {"times":[]} passed one gate
// and failed the other. An explicit empty array counts as ABSENT: [] states
// no times, so there is nothing in it to honour, and whether a client omits
// the key or serializes an empty list is a choice its JSON encoder makes
// (Go's own omitempty drops it, JSON.stringify keeps it) rather than a
// difference in what the operator asked for. Reading [] as ABSENT cannot
// loosen a floor: mode "times" never reaches this function — validateTimes
// and validateWeekdays refuse an empty agenda there by count.
func carriesAgendaDays(cfg JobConfig) bool {
	return len(cfg.Times) > 0 || len(cfg.Weekdays) > 0
}

func validateTimes(job string, times []string) error {
	if len(times) < MinTimes || len(times) > MaxTimes {
		return fmt.Errorf("%s needs between %d and %d wall times — the floor is one run per scheduled day, never zero", job, MinTimes, MaxTimes)
	}
	seen := map[string]bool{}
	for _, raw := range times {
		a, err := ParseAnchor(raw)
		if err != nil {
			return fmt.Errorf("%s time %q: %w", job, raw, err)
		}
		if a.Weekly {
			return fmt.Errorf("%s time %q: the weekday belongs in \"weekdays\", not in the time", job, raw)
		}
		if seen[a.String()] {
			return fmt.Errorf("%s time %q repeats", job, raw)
		}
		seen[a.String()] = true
	}
	return nil
}

func validateWeekdays(job string, days []string, minDays int) error {
	if len(days) == 0 {
		return fmt.Errorf("%s needs at least %d weekday(s) — an agenda that fires on no day is the job switched off", job, minDays)
	}
	seen := map[time.Weekday]bool{}
	for _, raw := range days {
		wd, ok := weekdays[strings.ToLower(strings.TrimSpace(raw))]
		if !ok {
			return fmt.Errorf("%s weekday %q is not one of sun..sat", job, raw)
		}
		if seen[wd] {
			return fmt.Errorf("%s weekday %q repeats", job, raw)
		}
		seen[wd] = true
	}
	if len(seen) < minDays {
		return fmt.Errorf("%s needs at least %d weekdays, got %d", job, minDays, len(seen))
	}
	return nil
}

// normalized translates a document written before the unified shape into it,
// so a row an older backend stored — or one hand-written in SQL — is honoured
// instead of silently degrading the job to the env baseline. A document that
// already carries a mode is returned untouched.
func (c JobConfig) normalized(job string) JobConfig {
	if c.Mode != "" {
		return c
	}
	out := JobConfig{Mode: modeTimes}
	if job == JobUserZip {
		// Only user_zip may carry it; a relic that did on another job would
		// normalize into a row the validator refuses forever.
		out.Enabled = c.Enabled
	}
	if out.Enabled != nil && !*out.Enabled {
		// The previous vocabulary let a row say "switched off" and still
		// write down the agenda it would have followed; the unified one
		// refuses that pair. Carrying it across would make the row invalid,
		// fall the job back to the env baseline and start it running again —
		// the exact opposite of what the operator asked for.
		return out
	}
	switch {
	case c.IntervalMin != 0:
		// Fields, never a fresh struct: replacing out here silently dropped
		// the Enabled decided above.
		out.Mode, out.IntervalMin = modeInterval, c.IntervalMin
	case len(c.Times) > 0:
		out.Times, out.Weekdays = c.Times, everyWeekdayName()
	case c.Time != "":
		out.Times = []string{c.Time}
		if c.Weekday != "" {
			out.Weekdays = []string{strings.ToLower(c.Weekday)}
		} else {
			out.Weekdays = everyWeekdayName()
		}
	}
	return out
}

func everyWeekdayName() []string {
	out := make([]string, 0, len(weekdayNames))
	out = append(out, weekdayNames[:]...)
	return out
}

// Timing is one job's effective runtime schedule after merging the env
// baseline with the database row: wall times crossed with a weekday set, or
// an interval, with Source recording which side won so the heartbeat — and
// through it the UI — can say where the agenda came from.
type Timing struct {
	Anchors  []Anchor       // wall times only; the days are Weekdays'
	Weekdays []time.Weekday // empty = every day
	Interval time.Duration
	Source   string // "db" | "env"
}

// Enabled reports whether the timing schedules anything at all.
func (t Timing) Enabled() bool { return len(t.Anchors) > 0 || t.Interval > 0 }

// days is the weekday set, sorted and deduplicated; empty means every day.
func (t Timing) days() []time.Weekday {
	if len(t.Weekdays) == 0 {
		return []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday}
	}
	seen := map[time.Weekday]bool{}
	out := make([]time.Weekday, 0, len(t.Weekdays))
	for _, d := range t.Weekdays {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (t Timing) daySet() map[time.Weekday]bool {
	set := map[time.Weekday]bool{}
	for _, d := range t.days() {
		set[d] = true
	}
	return set
}

// Next returns the earliest firing instant strictly after now across the times
// × weekdays, in now's location — the dump may carry several times a day and
// the scheduler sleeps until the nearest one.
//
// It is recomputed after every firing rather than derived by adding fixed
// intervals — that is what absorbs DST: on a transition day a time may fire
// twice or be skipped, which is documented and accepted (catch-up covers the
// skip, and a duplicate run is harmless — the second is fast and retention
// prunes it).
func (t Timing) Next(now time.Time) time.Time {
	days := t.daySet()
	var best time.Time
	for _, a := range t.Anchors {
		cand := time.Date(now.Year(), now.Month(), now.Day(), a.Hour, a.Minute, 0, 0, now.Location())
		// At most eight steps: the day set is never empty, so a scheduled
		// weekday is reached within a week of the first candidate.
		for i := 0; i < 8; i++ {
			if cand.After(now) && days[cand.Weekday()] {
				if best.IsZero() || cand.Before(best) {
					best = cand
				}
				break
			}
			cand = cand.AddDate(0, 0, 1)
		}
	}
	return best
}

// MaxGap is the longest legitimate silence between consecutive firings — the
// interval the catch-up decision and the staleness contract compare against.
// For an interval timing it is the interval; otherwise it is the widest
// wraparound gap of the WEEK GRID (weekdays × times). The week grid subsumes
// every case a special-cased daily/weekly split used to handle: one time on
// one weekday yields seven days on its own, and a twice-daily agenda
// restricted to three weekdays yields the real fri→mon silence rather than
// the 12h a minutes-since-midnight calculation would report.
func (t Timing) MaxGap() time.Duration {
	if t.Interval > 0 {
		return t.Interval
	}
	if len(t.Anchors) == 0 {
		return 0
	}
	const week = 7 * 24 * 60
	mins := make([]int, 0, len(t.Anchors)*7)
	for _, d := range t.days() {
		for _, a := range t.Anchors {
			mins = append(mins, int(d)*24*60+a.Hour*60+a.Minute)
		}
	}
	sort.Ints(mins)
	widest := mins[0] + week - mins[len(mins)-1]
	for i := 1; i < len(mins); i++ {
		if gap := mins[i] - mins[i-1]; gap > widest {
			widest = gap
		}
	}
	return time.Duration(widest) * time.Minute
}

// Due is the boot catch-up decision: run now when the job never succeeded, or
// when the last success is more than one full gap plus 25% grace behind. The
// grace keeps a restart minutes after a firing from double-running a job that
// in fact succeeded on time. Interval and wall-time agendas share it — MaxGap
// is what tells them apart.
func (t Timing) Due(now, lastSuccess time.Time) bool {
	if !t.Enabled() {
		return false
	}
	if lastSuccess.IsZero() {
		return true
	}
	gap := t.MaxGap()
	return now.Sub(lastSuccess) > gap+gap/4
}

// PreviousSlot is the most recent firing at or before now — the slot a
// catch-up run satisfies (scheduled_for's contract, migration 000040), which
// must be the MISSED instant, not the moment the agent happened to restart.
func (t Timing) PreviousSlot(now time.Time) time.Time {
	days := t.daySet()
	var best time.Time
	for _, a := range t.Anchors {
		cand := time.Date(now.Year(), now.Month(), now.Day(), a.Hour, a.Minute, 0, 0, now.Location())
		for i := 0; i < 8; i++ {
			if !cand.After(now) && days[cand.Weekday()] {
				if cand.After(best) {
					best = cand
				}
				break
			}
			cand = cand.AddDate(0, 0, -1)
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
	times := make([]string, 0, len(t.Anchors))
	for _, a := range t.Anchors {
		times = append(times, a.String())
	}
	days := t.days()
	if len(days) == len(weekdayNames) {
		return strings.Join(times, ", ")
	}
	names := make([]string, 0, len(days))
	for _, d := range days {
		names = append(names, weekdayNames[d])
	}
	return strings.Join(times, ", ") + " · " + strings.Join(names, ", ")
}

// ToConfig is the structured form of this timing — what lets the admin form
// open pre-filled with the env baseline instead of a blank agenda. A timing
// with no weekday restriction emits all seven explicitly, so the form shows a
// concrete set rather than an empty one the owner reads as "no days".
func (t Timing) ToConfig() JobConfig {
	if !t.Enabled() {
		return JobConfig{}
	}
	if t.Interval > 0 {
		return JobConfig{Mode: modeInterval, IntervalMin: int(t.Interval.Minutes())}
	}
	cfg := JobConfig{Mode: modeTimes}
	for _, a := range t.Anchors {
		cfg.Times = append(cfg.Times, a.String())
	}
	for _, d := range t.days() {
		cfg.Weekdays = append(cfg.Weekdays, weekdayNames[d])
	}
	return cfg
}

// timingFromConfig turns a validated row into a Timing. Only reached after
// ValidateJobConfig, so parse errors here are impossible by construction —
// they still surface (as a disabled timing) rather than panic.
func timingFromConfig(cfg JobConfig) Timing {
	t := Timing{Source: "db"}
	if cfg.Enabled != nil && !*cfg.Enabled {
		return t
	}
	switch cfg.Mode {
	case modeInterval:
		t.Interval = time.Duration(cfg.IntervalMin) * time.Minute
	case modeTimes:
		for _, raw := range cfg.Times {
			if a, err := ParseAnchor(raw); err == nil {
				t.Anchors = append(t.Anchors, timeOnly(a))
			}
		}
		for _, raw := range cfg.Weekdays {
			if wd, ok := weekdays[strings.ToLower(strings.TrimSpace(raw))]; ok {
				t.Weekdays = append(t.Weekdays, wd)
			}
		}
	}
	return t
}

// envTiming is the env-baseline Timing for one job. A weekly env anchor
// becomes a one-day weekday set: the days live on the Timing now.
func envTiming(job string, cfg Config) Timing {
	t := Timing{Source: "env"}
	var anchor Anchor
	switch job {
	case JobDump:
		anchor = cfg.DumpAt
	case JobDrill:
		anchor = cfg.DrillAt
	case JobUserZip:
		anchor = cfg.UserZipAt
	case JobMirror:
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
	t.Anchors = []Anchor{timeOnly(anchor)}
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
func EffectiveTiming(job string, cfg Config, row *JobConfig) Timing {
	env := envTiming(job, cfg)
	if row == nil || ValidateJobConfig(job, *row) != nil {
		return env
	}
	if job == JobMirror && !cfg.MirrorEnabled() {
		return env
	}
	return timingFromConfig(*row)
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
	// Malformed carries why the stored document could not be decoded at all,
	// empty when it decoded fine. It is not an error return because ONE
	// unreadable row must not decide the agenda of the other three jobs — see
	// Load. The row still comes back so the fallback stays visible.
	Malformed string `json:"malformed,omitempty"`
}

// Load returns the stored rows by job, each normalized into the unified shape
// so a document written before it is honoured rather than degraded. A row
// whose document still does not validate is returned anyway — EffectiveTiming
// refuses it and the caller logs; hiding it here would make the fallback
// invisible.
//
// That tolerance is PER ROW, including for a document json cannot decode at
// all. Failing the whole read on one bad row was the shape this had, and it
// contradicted the paragraph above in the worst direction: a hand-written
// {"interval_min": "nightly"} made every sync tick error, so all four jobs
// silently kept whatever timing they already had and nothing said why. The
// unreadable row now comes back with Malformed set and a config the floors
// refuse, which is exactly the path an invalid-but-decodable row takes.
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
			// Zeroed, not left half-decoded: a partially populated config is a
			// document nobody wrote, and the floors must judge the row on what
			// it says, which here is nothing.
			r.Config, r.Malformed = JobConfig{}, err.Error()
			out[r.Job] = r
			continue
		}
		r.Config = r.Config.normalized(r.Job)
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
	// Baseline is the ENV agenda as a document, so the admin form can open
	// pre-filled on it: env is the first option, the database row is the
	// override. Zero for a job this process cannot run.
	Baseline JobConfig `json:"baseline"`
	// Destination names WHERE this job's objects go in the external bucket.
	// Nil for a job that never touches it.
	Destination *Destination `json:"destination,omitempty"`
}

// Destination is the external bucket as an ADDRESS, never as an access.
//
// It exists because an agenda the operator cannot aim is an agenda they cannot
// verify: "copies the objects to the external bucket" says nothing about which
// bucket, and the endpoint is the field most likely to point somewhere other
// than intended — at the same host as the origin, say, which is a mirror that
// survives no failure the mirror exists for.
//
// The three fields are deliberately the three that are NOT secret. INV-171
// keeps `BACKUP_S3_ACCESS_KEY`/`BACKUP_S3_SECRET_KEY` inside this process, and
// this struct is rendered on an administration screen — so it carries the
// address and stops there.
type Destination struct {
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	// Prefix is the job's key namespace inside the bucket, which is what makes
	// the line actionable: it is where the operator looks with their own S3
	// client to confirm the copies are landing.
	Prefix string `json:"prefix"`
}

// AgentState is the heartbeat row.
type AgentState struct {
	SeenAt  time.Time `json:"seen_at"`
	Version string    `json:"version"`
	// SchemaVersion is the agent's own RequiredSchemaVersion: the newest
	// migration THIS build knows how to read. It is the honest signal for
	// build skew, and Version is not — a version string says what was shipped,
	// not what the process understands.
	//
	// The failure it exists to surface is silent: RequiredSchemaVersion is a
	// FLOOR, so an agent built against 42 boots happily on schema 43 and reads
	// the unified rows honouring `times` while ignoring `weekdays` entirely.
	// It over-runs, which is the safe direction, and says nothing. Zero means
	// a heartbeat written before this field existed — not a match.
	SchemaVersion int                  `json:"schema_version,omitempty"`
	Jobs          map[string]JobReport `json:"jobs"`
}

// Heartbeat upserts the single agent-state row.
func (s *ScheduleStore) Heartbeat(ctx context.Context, state AgentState) error {
	caps, err := json.Marshal(map[string]any{
		"version":        state.Version,
		"schema_version": state.SchemaVersion,
		"jobs":           state.Jobs,
	})
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
		Version       string               `json:"version"`
		SchemaVersion int                  `json:"schema_version"`
		Jobs          map[string]JobReport `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return AgentState{}, false, fmt.Errorf("backupagent: agent capabilities: %w", err)
	}
	return AgentState{
		SeenAt:        seenAt,
		Version:       doc.Version,
		SchemaVersion: doc.SchemaVersion,
		Jobs:          doc.Jobs,
	}, true, nil
}

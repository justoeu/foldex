package backupagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RequiredSchemaVersion is the migration the agent needs. The agent never
// runs migrations — the backend owns the schema — so boot fails with an
// instruction instead of a missing-table error mid-job.
//
// Deliberately NOT db.RequiredSchemaVersion: that number tracks what the
// BACKEND reads, and moves whenever any backend query gains a dependency.
// This one moves only for tables the agent itself touches: 40 for backup_run,
// 42 for backup_schedule (read) and backup_agent_state (heartbeat, written),
// 43 for the unified shape of backup_schedule.config.
const RequiredSchemaVersion = 43

const janitorInterval = time.Hour

// jobSpec is one entry in the agent's registry. The WHEN no longer lives
// here: schedules are Timings resolved from the env baseline plus the
// backup_schedule table (ADR-44), swapped live by the sync loop — a spec is
// only the job's name and its work.
type jobSpec struct {
	name string
	// run receives the backup_run row id it executes under: the drill needs
	// it to stamp drill_of_run_id as soon as it picks a source, so even a run
	// that fails mid-pipeline records WHICH dump it was validating.
	run func(ctx context.Context, runID int64) (*Artifact, map[string]any, string, error)
}

// Agent wires the jobs to their schedule and to backup_run.
type Agent struct {
	cfg     Config
	pool    *pgxpool.Pool
	store   Uploader
	runs    *RunStore
	sched   *ScheduleStore
	metrics *Metrics
	logger  *slog.Logger
	jobs    []jobSpec

	// timings is the live agenda: env baseline merged with the database rows,
	// refreshed by the sync loop. schedChange is closed (and replaced) on
	// every swap so the schedule loops recompute their timers mid-sleep.
	schedMu     sync.RWMutex
	timings     map[string]Timing
	schedChange chan struct{}
	// timingOverrides pins a job's Timing past the sync loop — tests need
	// sub-second cadences no row or env var can express.
	timingOverrides map[string]Timing

	// catchUpJitter delays a boot catch-up run so a `compose up` does not fire
	// a dump into a half-started stack. A seam because it is minutes long and
	// probabilistic — the one knob that made the catch-up path untestable, and
	// worse: a test that enables a schedule right after Start can lose the
	// race into bootCatchUp and sleep production minutes inside a 10s test.
	catchUpJitter func() time.Duration

	// skewWarning is checked from Start, not the constructor: it does I/O
	// (SHOW server_version + exec pg_dump), and a constructor that can hang on
	// an unreachable database hides the failure CheckSchema reports cleanly.
	skewWarning func(context.Context) string

	cancel   context.CancelFunc
	stopOnce sync.Once
	wg       sync.WaitGroup
	http     *http.Server
	httpAddr string // bound address, once serveHTTP has a listener
}

// New builds the agent. store carries the external bucket; mirrorSource is a
// second client pointed at the RustFS origin, nil when the mirror is off —
// the mirror registers only when both the interval and the source exist.
func New(cfg Config, pool *pgxpool.Pool, store Uploader, mirrorSource SourceBucket, logger *slog.Logger) (*Agent, error) {
	a := &Agent{
		cfg:         cfg,
		pool:        pool,
		store:       store,
		runs:        NewRunStore(pool),
		sched:       NewScheduleStore(pool),
		metrics:     NewMetrics(),
		logger:      logger,
		schedChange: make(chan struct{}),
		catchUpJitter: func() time.Duration {
			return time.Minute + rand.N(4*time.Minute)
		},
	}
	dump, err := NewDumpJob(cfg, pool, store, logger)
	if err != nil {
		return nil, err
	}
	a.jobs = append(a.jobs, jobSpec{name: JobDump, run: func(ctx context.Context, _ int64) (*Artifact, map[string]any, string, error) {
		return dump.Run(ctx)
	}})
	a.skewWarning = dump.VersionSkewWarning

	drill, err := NewDrillJob(cfg, a.runs, store, logger)
	if err != nil {
		return nil, err
	}
	// Registered even with no schedule: the registry is also what the
	// requested-claim loop iterates, so an operator can trigger a manual
	// drill from the admin surface without scheduling one.
	a.jobs = append(a.jobs, jobSpec{name: JobDrill, run: drill.Run})

	if cfg.MirrorEnabled() {
		if mirrorSource == nil {
			// Enabled-but-sourceless must refuse, not silently drop the job:
			// a mirror the operator turned on and never runs is the mailer
			// incident with a new name.
			return nil, fmt.Errorf("backupagent: BACKUP_MIRROR_INTERVAL_MIN is set but no source bucket client was provided")
		}
		mirror, err := NewMirrorJob(cfg, pool, a.runs, mirrorSource, store, logger)
		if err != nil {
			return nil, err
		}
		a.jobs = append(a.jobs, jobSpec{name: JobMirror, run: func(ctx context.Context, _ int64) (*Artifact, map[string]any, string, error) {
			return mirror.Run(ctx)
		}})
	}
	a.timings = a.computeTimings(nil)
	return a, nil
}

// RegisterJob appends a job to the registry, between New and Start. It exists
// for jobs whose dependencies New does not carry: user_zip needs a
// backup.Service over the SOURCE bucket, which only the binary constructs.
func (a *Agent) RegisterJob(name string, run func(ctx context.Context) (*Artifact, map[string]any, string, error)) {
	a.jobs = append(a.jobs, jobSpec{name: name, run: func(ctx context.Context, _ int64) (*Artifact, map[string]any, string, error) {
		return run(ctx)
	}})
	a.setTimings(a.computeTimings(nil))
}

// registered reports whether a job is in the registry — capability for
// user_zip, whose Service only the binary decides to build.
func (a *Agent) registered(name string) bool {
	for _, s := range a.jobs {
		if s.name == name {
			return true
		}
	}
	return false
}

// capability answers whether THIS process can run a job at all, and why not.
// Capabilities come from the environment alone (credentials, the identity,
// the constructed clients) — the database can move a schedule, never conjure
// a capability (INV-173).
func (a *Agent) capability(name string) (bool, string) {
	switch name {
	case JobDrill:
		if a.cfg.AgeIdentityFile == "" {
			return false, "no_identity"
		}
	case JobMirror:
		if !a.cfg.MirrorEnabled() {
			return false, "mirror_off"
		}
	case JobUserZip:
		if !a.registered(JobUserZip) {
			return false, "no_source_credentials"
		}
	}
	return true, ""
}

// computeTimings resolves the live agenda: per registered job, the env
// baseline merged with its backup_schedule row, capability-gated, then test
// overrides. A row for an incapable job is logged and ignored — honouring it
// would schedule work the process cannot perform.
func (a *Agent) computeTimings(rows map[string]ScheduleRow) map[string]Timing {
	out := make(map[string]Timing, len(a.jobs))
	for _, spec := range a.jobs {
		var row *JobConfig
		if r, ok := rows[spec.name]; ok {
			cfg := r.Config
			switch {
			// A document json could not read at all says nothing the floors
			// could judge, so report what actually went wrong rather than the
			// downstream "needs mode" the empty config would produce.
			case r.Malformed != "":
				a.logger.Warn("schedule row could not be decoded; using the env baseline",
					"job", spec.name, "err", r.Malformed)
			default:
				if err := ValidateJobConfig(spec.name, cfg); err != nil {
					a.logger.Warn("schedule row is invalid; using the env baseline", "job", spec.name, "err", err)
				} else {
					row = &cfg
				}
			}
		}
		if capable, reason := a.capability(spec.name); !capable {
			if row != nil {
				a.logger.Warn("schedule row for a job this agent cannot run; ignoring", "job", spec.name, "reason", reason)
			}
			out[spec.name] = Timing{Source: "env"}
			continue
		}
		out[spec.name] = EffectiveTiming(spec.name, a.cfg, row)
	}
	a.schedMu.RLock()
	for job, t := range a.timingOverrides {
		out[job] = t
	}
	a.schedMu.RUnlock()
	return out
}

// timing returns the current Timing for a job.
func (a *Agent) timing(name string) Timing {
	a.schedMu.RLock()
	defer a.schedMu.RUnlock()
	return a.timings[name]
}

// changeCh returns the channel closed on the next agenda swap.
func (a *Agent) changeCh() <-chan struct{} {
	a.schedMu.RLock()
	defer a.schedMu.RUnlock()
	return a.schedChange
}

// setTimings swaps the agenda and wakes every schedule loop.
func (a *Agent) setTimings(next map[string]Timing) {
	a.schedMu.Lock()
	a.timings = next
	close(a.schedChange)
	a.schedChange = make(chan struct{})
	a.schedMu.Unlock()
}

// forceTiming pins one job's Timing past the sync loop (tests only).
func (a *Agent) forceTiming(name string, t Timing) {
	a.schedMu.Lock()
	if a.timingOverrides == nil {
		a.timingOverrides = map[string]Timing{}
	}
	a.timingOverrides[name] = t
	a.schedMu.Unlock()
	a.setTimings(a.computeTimings(nil))
}

func timingsEqual(x, y map[string]Timing) bool {
	if len(x) != len(y) {
		return false
	}
	for job, t := range x {
		o, ok := y[job]
		if !ok || o.Source != t.Source || o.String() != t.String() {
			return false
		}
	}
	return true
}

// agentState renders the heartbeat: per-job capability and the agenda this
// process is actually following — the honesty layer the schedule UI needs
// before it lets an owner agenda a job the agent cannot run.
func (a *Agent) agentState() AgentState {
	jobs := make(map[string]JobReport, len(a.jobs))
	for _, spec := range a.jobs {
		capable, reason := a.capability(spec.name)
		t := a.timing(spec.name)
		jobs[spec.name] = JobReport{
			Capable:  capable,
			Reason:   reason,
			Source:   t.Source,
			Schedule: t.String(),
			Baseline: envTiming(spec.name, a.cfg).ToConfig(),
		}
	}
	// Unregistered jobs are reported anyway: absent from the map they would
	// render as "unknown" instead of "unavailable, and here is why" — and an
	// absent mirror let the UI offer editors for a row no process would ever
	// read.
	if !a.registered(JobUserZip) {
		jobs[JobUserZip] = JobReport{Capable: false, Reason: "no_source_credentials", Source: "env", Schedule: "disabled"}
	}
	if !a.registered(JobMirror) {
		jobs[JobMirror] = JobReport{Capable: false, Reason: "mirror_off", Source: "env", Schedule: "disabled"}
	}
	return AgentState{SeenAt: time.Now(), Version: a.cfg.Version, Jobs: jobs}
}

// CheckSchema gates the boot on the agent's own migrations being applied.
func (a *Agent) CheckSchema(ctx context.Context) error {
	v, err := a.runs.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if v < RequiredSchemaVersion {
		return fmt.Errorf("backupagent: database schema is at %d, need >= %d — run backend migrations first (make migrate-up)", v, RequiredSchemaVersion)
	}
	return nil
}

// Start launches the schedule loops, the schedule/heartbeat sync, the
// requested-claim poller, the janitor and the metrics server. Same lifecycle
// contract as the other workers: Start(ctx) then Stop(), idempotent.
func (a *Agent) Start(ctx context.Context) {
	ctx, a.cancel = context.WithCancel(ctx)

	if a.skewWarning != nil {
		skewCtx, done := context.WithTimeout(ctx, 10*time.Second)
		if warning := a.skewWarning(skewCtx); warning != "" {
			a.logger.Warn(warning)
		}
		done()
	}
	if _, err := a.runs.ExpireStale(ctx, a.cfg.StaleRunTTL()); err != nil {
		a.logger.Warn("boot janitor failed", "err", err)
	}
	for _, spec := range a.jobs {
		if last, err := a.runs.LastSuccess(ctx, spec.name); err == nil {
			a.metrics.SeedLastSuccess(spec.name, last)
		}
	}

	// One synchronous sync before the loops: the very first timers must
	// already honour the stored agenda, not fire once on the env baseline and
	// correct themselves half a minute later.
	a.syncSchedule(ctx)

	for _, spec := range a.jobs {
		a.wg.Add(1)
		go a.scheduleLoop(ctx, spec)
	}
	a.wg.Add(3)
	go a.scheduleSyncLoop(ctx)
	go a.requestedLoop(ctx)
	go a.janitorLoop(ctx)

	a.serveHTTP()
}

// Stop cancels the loops and waits for in-flight work to record its outcome.
func (a *Agent) Stop() {
	a.stopOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		if a.http != nil {
			shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			_ = a.http.Shutdown(shutCtx)
		}
		a.wg.Wait()
	})
}

// syncSchedule refreshes the agenda from backup_schedule and writes the
// heartbeat. A failed load keeps the current agenda — degrading to yesterday's
// schedule beats degrading to no schedule.
func (a *Agent) syncSchedule(ctx context.Context) {
	rows, err := a.sched.Load(ctx)
	if err != nil {
		a.logger.Warn("schedule load failed; keeping the current agenda", "err", err)
	} else {
		next := a.computeTimings(rows)
		a.schedMu.RLock()
		changed := !timingsEqual(a.timings, next)
		a.schedMu.RUnlock()
		if changed {
			a.setTimings(next)
			for job, t := range next {
				a.logger.Info("agenda updated", "job", job, "schedule", t.String(), "source", t.Source)
			}
		}
	}
	if err := a.sched.Heartbeat(ctx, a.agentState()); err != nil {
		a.logger.Warn("heartbeat failed", "err", err)
	}
}

// scheduleSyncLoop re-syncs on the requested-poll cadence: the same ~30s that
// bounds how stale the manual-trigger channel can be also bounds how long an
// owner's agenda edit takes to reach the timers, and the heartbeat rides
// along so "agent last seen" stays fresh in the band.
func (a *Agent) scheduleSyncLoop(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(a.cfg.RequestedPoll())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.syncSchedule(ctx)
		}
	}
}

// scheduleLoop drives one job from its live Timing: sleep to the next slot,
// run, recompute — and recompute early whenever the agenda swaps under it.
func (a *Agent) scheduleLoop(ctx context.Context, spec jobSpec) {
	defer a.wg.Done()

	a.bootCatchUp(ctx, spec)

	var lastFire time.Time
	// Anchors the first interval wait: without it, every agenda swap of ANY
	// job re-entered this loop and reset a never-fired interval's countdown
	// to "now + interval", postponing the first run indefinitely under
	// frequent edits.
	var intervalStart time.Time
	for {
		// changeCh BEFORE timing, and the order is load-bearing: a swap that
		// lands between the two reads closes the channel this loop is about
		// to select on, so it wakes and re-reads. Read the other way around,
		// that swap closed a channel nobody held — and a loop parked on the
		// "no schedule" branch would sleep on the NEW channel waiting for an
		// edit that already happened, keeping a just-enabled job dormant
		// until the next edit or restart.
		change := a.changeCh()
		t := a.timing(spec.name)
		if !t.Enabled() {
			a.logger.Info("job has no schedule; waiting for one", "job", spec.name)
			select {
			case <-ctx.Done():
				return
			case <-change:
				continue
			}
		}
		var fireAt time.Time
		if t.Interval > 0 {
			base := lastFire
			if base.IsZero() {
				if intervalStart.IsZero() {
					intervalStart = time.Now()
				}
				base = intervalStart
			}
			fireAt = base.Add(t.Interval)
			if !fireAt.After(time.Now()) {
				// The interval shrank past the elapsed wait: fire promptly
				// instead of stretching the old cadence one more period.
				fireAt = time.Now()
			}
		} else {
			fireAt = t.Next(time.Now())
		}
		a.logger.Info("next run scheduled", "job", spec.name, "at", fireAt, "source", t.Source)
		timer := time.NewTimer(time.Until(fireAt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-change:
			timer.Stop()
			continue
		case <-timer.C:
			lastFire = time.Now()
			a.execute(ctx, spec, fireAt, 0)
		}
	}
}

// bootCatchUp runs a job immediately after boot when its last success is more
// than one full gap (plus grace) behind. State lives in backup_run, so the
// decision is correct across restarts with no local file — the container
// stays disposable. The jitter keeps a `compose up` from firing a dump into a
// half-started stack; waitReady then holds until the database answers.
func (a *Agent) bootCatchUp(ctx context.Context, spec jobSpec) {
	t := a.timing(spec.name)
	if !t.Enabled() {
		return
	}
	last, err := a.runs.LastSuccess(ctx, spec.name)
	if err != nil {
		a.logger.Error("catch-up decision failed", "job", spec.name, "err", err)
		return
	}
	if !t.Due(time.Now(), last) {
		return
	}
	// There is no missed wall-clock slot to reconstruct for an interval job:
	// scheduled_for is simply the moment it fires.
	slot := time.Now()
	if t.Interval == 0 {
		slot = t.PreviousSlot(time.Now())
	}
	jitter := a.catchUpJitter()
	a.logger.Info("catch-up scheduled", "job", spec.name, "in", jitter.Round(time.Second))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}
	if a.waitReady(ctx) {
		a.execute(ctx, spec, slot, 0)
	}
}

// requestedLoop claims manual 'requested' rows the backend inserted. The
// admin button is an INSERT, not an RPC: no new authenticated surface on the
// agent, auditing for free (the row IS the record), and honest degradation —
// with no agent alive the request ages visibly instead of timing out.
func (a *Agent) requestedLoop(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(a.cfg.RequestedPoll())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, spec := range a.jobs {
				id, ok, err := a.runs.ClaimRequested(ctx, spec.name)
				if err != nil {
					a.logger.Error("claim requested", "job", spec.name, "err", err)
					continue
				}
				if ok {
					a.execute(ctx, spec, time.Now(), id)
				}
			}
		}
	}
}

func (a *Agent) janitorLoop(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := a.runs.ExpireStale(ctx, a.cfg.StaleRunTTL()); err != nil {
				a.logger.Warn("janitor failed", "err", err)
			} else if n > 0 {
				a.logger.Warn("janitor expired stale runs", "count", n)
			}
		}
	}
}

// execute runs one job occurrence end to end: advisory lock, backup_run row,
// the job itself, outcome and metrics. claimedID != 0 means a 'requested' row
// was already promoted to running and owns the slot.
func (a *Agent) execute(ctx context.Context, spec jobSpec, scheduledFor time.Time, claimedID int64) {
	release, ok, err := acquireJobLock(ctx, a.pool)
	if err != nil {
		a.logger.Error("advisory lock", "job", spec.name, "err", err)
		return
	}
	if !ok {
		a.logger.Warn("another agent holds the job lock; skipping slot", "job", spec.name)
		if claimedID != 0 {
			_ = a.runs.Fail(ctx, claimedID, ReasonLockBusy)
		}
		return
	}
	defer release()

	id := claimedID
	if id == 0 {
		id, err = a.runs.Begin(ctx, spec.name, scheduledFor)
		if errors.Is(err, ErrAlreadyRunning) {
			a.logger.Warn("a run is already recorded as running; skipping slot", "job", spec.name)
			return
		}
		if err != nil {
			a.logger.Error("record run", "job", spec.name, "err", err)
			return
		}
	}

	started := time.Now()
	a.logger.Info("run started", "job", spec.name, "run_id", id)
	artifact, meta, reason, runErr := spec.run(ctx, id)
	duration := time.Since(started)

	// Outcomes are recorded on a fresh context: the run's ctx is exactly what
	// shutdown cancels, and an unrecorded outcome is a stale 'running' row.
	recordCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()

	if runErr != nil {
		if ctx.Err() != nil {
			reason = ReasonShutdown
		}
		a.logger.Error("run failed", "job", spec.name, "run_id", id, "reason", reason, "err", runErr)
		if err := a.runs.Fail(recordCtx, id, reason); err != nil {
			a.logger.Error("record failure", "job", spec.name, "err", err)
		}
		consecutive, _ := a.runs.ConsecutiveFailures(recordCtx, spec.name)
		a.metrics.ObserveFailure(spec.name, duration, consecutive)
		return
	}

	if meta == nil {
		meta = map[string]any{}
	}
	// One measurement feeds both the row and the gauge — two clocks would let
	// them disagree.
	meta["duration_ms"] = duration.Milliseconds()
	if err := a.runs.Succeed(recordCtx, id, artifact, meta); err != nil {
		a.logger.Error("record success", "job", spec.name, "err", err)
		return
	}
	var artifactBytes int64
	if artifact != nil {
		artifactBytes = artifact.Bytes
		if artifact.Mirror != nil {
			// The mirror ships no single artifact; what the gauge should say
			// is how much data the pass actually moved.
			artifactBytes = artifact.Mirror.BytesCopied
		}
	}
	a.metrics.ObserveSuccess(spec.name, time.Now(), duration, artifactBytes)
	a.logger.Info("run succeeded", "job", spec.name, "run_id", id,
		"duration", duration.Round(time.Millisecond), "artifact_bytes", artifactBytes)
}

// waitReady polls the database AND the backup target until both answer or
// ctx dies. Gating catch-up on the store too keeps an S3 blip at `compose up`
// from turning the catch-up into an immediate failed(upload_failed) row.
func (a *Agent) waitReady(ctx context.Context) bool {
	for {
		pingCtx, done := context.WithTimeout(ctx, 5*time.Second)
		err := a.pool.Ping(pingCtx)
		if err == nil {
			err = a.storeReady(pingCtx)
		}
		done()
		if err == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Second):
		}
	}
}

// errStopWalk stops a readiness listing after proving the store answers.
var errStopWalk = errors.New("stop")

func (a *Agent) storeReady(ctx context.Context) error {
	err := a.store.WalkObjects(ctx, dumpKeyPrefix, func(ObjectInfo) error { return errStopWalk })
	if errors.Is(err, errStopWalk) {
		return nil
	}
	return err // nil on an empty listing is also ready
}

func (a *Agent) serveHTTP() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, done := context.WithTimeout(r.Context(), 2*time.Second)
		defer done()
		if err := a.pool.Ping(pingCtx); err != nil {
			http.Error(w, `{"status":"degraded"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/metrics", a.metrics.Handler(a.cfg.MetricsToken))

	a.http = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	// Listen synchronously, serve in the goroutine: a bad address fails HERE,
	// at boot, instead of as a log line racing the "ready" message — and the
	// bound address is knowable (":0" in tests, and in any setup that lets
	// the OS pick).
	listener, err := net.Listen("tcp", a.cfg.MetricsAddr)
	if err != nil {
		a.logger.Error("metrics server", "err", err)
		return
	}
	a.httpAddr = listener.Addr().String()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := a.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Error("metrics server", "err", err)
		}
	}()
}

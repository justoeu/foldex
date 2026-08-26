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

// RequiredSchemaVersion is the migration the agent needs (backup_run). The
// agent never runs migrations — the backend owns the schema — so boot fails
// with an instruction instead of a missing-table error mid-job.
const RequiredSchemaVersion = 40

const janitorInterval = time.Hour

// jobSpec is one entry in the agent's registry. Adding a job in a later PR is
// adding an entry here (drill, mirror, user_zip) — the scheduler, the
// requested-claim loop, the janitor and the metrics all iterate the table and
// need no edits (SDD-OPS-BACKUP §15).
type jobSpec struct {
	name   string
	anchor Anchor
	// interval schedules by cadence instead of wall-clock anchor (mirror).
	// Exactly one of anchor/interval is set; interval > 0 wins.
	interval time.Duration
	// run receives the backup_run row id it executes under: the drill needs
	// it to stamp drill_of_run_id as soon as it picks a source, so even a run
	// that fails mid-pipeline records WHICH dump it was validating.
	run func(ctx context.Context, runID int64) (*Artifact, map[string]any, string, error)
}

// enabled reports whether the spec has any schedule at all. A disabled job
// still answers 'requested' rows — the operator's button works even when the
// cadence is off.
func (s jobSpec) enabled() bool { return s.anchor.Enabled() || s.interval > 0 }

// Agent wires the jobs to their schedule and to backup_run.
type Agent struct {
	cfg     Config
	pool    *pgxpool.Pool
	store   Uploader
	runs    *RunStore
	metrics *Metrics
	logger  *slog.Logger
	jobs    []jobSpec

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
		cfg:     cfg,
		pool:    pool,
		store:   store,
		runs:    NewRunStore(pool),
		metrics: NewMetrics(),
		logger:  logger,
	}
	dump, err := NewDumpJob(cfg, pool, store, logger)
	if err != nil {
		return nil, err
	}
	a.jobs = append(a.jobs, jobSpec{name: JobDump, anchor: cfg.DumpAt, run: func(ctx context.Context, _ int64) (*Artifact, map[string]any, string, error) {
		return dump.Run(ctx)
	}})
	a.skewWarning = dump.VersionSkewWarning

	drill, err := NewDrillJob(cfg, a.runs, store, logger)
	if err != nil {
		return nil, err
	}
	// Registered even with no anchor: the registry is also what the
	// requested-claim loop iterates, so an operator can trigger a manual
	// drill from the admin surface without scheduling one.
	a.jobs = append(a.jobs, jobSpec{name: JobDrill, anchor: cfg.DrillAt, run: drill.Run})

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
		a.jobs = append(a.jobs, jobSpec{name: JobMirror, interval: cfg.MirrorInterval(), run: func(ctx context.Context, _ int64) (*Artifact, map[string]any, string, error) {
			return mirror.Run(ctx)
		}})
	}
	return a, nil
}

// RegisterJob appends a job to the registry, between New and Start. It exists
// for jobs whose dependencies New does not carry: user_zip needs a
// backup.Service over the SOURCE bucket, which only the binary constructs.
func (a *Agent) RegisterJob(name string, anchor Anchor, run func(ctx context.Context) (*Artifact, map[string]any, string, error)) {
	a.jobs = append(a.jobs, jobSpec{name: name, anchor: anchor, run: func(ctx context.Context, _ int64) (*Artifact, map[string]any, string, error) {
		return run(ctx)
	}})
}

// CheckSchema gates the boot on migration 000040 being applied.
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

// Start launches the schedule loops, the requested-claim poller, the janitor
// and the metrics server. Same lifecycle contract as the other workers:
// Start(ctx) then Stop(), idempotent.
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

	for _, spec := range a.jobs {
		if !spec.enabled() {
			a.logger.Info("job disabled (no schedule configured)", "job", spec.name)
			continue
		}
		a.wg.Add(1)
		go a.scheduleLoop(ctx, spec)
	}
	a.wg.Add(2)
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

func (a *Agent) scheduleLoop(ctx context.Context, spec jobSpec) {
	defer a.wg.Done()

	if spec.interval > 0 {
		a.intervalLoop(ctx, spec)
		return
	}

	// Boot catch-up: state lives in backup_run, so the decision is correct
	// across restarts with no local file — the container stays disposable.
	// The jitter keeps a `compose up` from firing a dump into a half-started
	// stack; waitReady then holds until the database answers.
	last, err := a.runs.LastSuccess(ctx, spec.name)
	if err != nil {
		a.logger.Error("catch-up decision failed", "job", spec.name, "err", err)
	} else if spec.anchor.Due(time.Now(), last) {
		jitter := time.Minute + rand.N(4*time.Minute)
		a.logger.Info("catch-up scheduled", "job", spec.name, "in", jitter.Round(time.Second))
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}
		if a.waitReady(ctx) {
			a.execute(ctx, spec, spec.anchor.PreviousSlot(time.Now()), 0)
		}
	}

	for {
		next := spec.anchor.Next(time.Now())
		a.logger.Info("next run scheduled", "job", spec.name, "at", next)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			a.execute(ctx, spec, next, 0)
		}
	}
}

// intervalLoop is the cadence-scheduled sibling of the anchor path: a plain
// ticker, with the same jittered, readiness-gated boot catch-up. There is no
// missed wall-clock slot to reconstruct for an interval job, so scheduled_for
// is simply the moment it fires.
func (a *Agent) intervalLoop(ctx context.Context, spec jobSpec) {
	last, err := a.runs.LastSuccess(ctx, spec.name)
	if err != nil {
		a.logger.Error("catch-up decision failed", "job", spec.name, "err", err)
	} else if intervalDue(time.Now(), last, spec.interval) {
		jitter := time.Minute + rand.N(4*time.Minute)
		a.logger.Info("catch-up scheduled", "job", spec.name, "in", jitter.Round(time.Second))
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}
		if a.waitReady(ctx) {
			a.execute(ctx, spec, time.Now(), 0)
		}
	}

	a.logger.Info("interval schedule active", "job", spec.name, "every", spec.interval)
	ticker := time.NewTicker(spec.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case fired := <-ticker.C:
			a.execute(ctx, spec, fired, 0)
		}
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

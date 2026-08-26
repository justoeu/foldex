package backupagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
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
	run    func(ctx context.Context) (*Artifact, map[string]any, string, error)
}

// Agent wires the jobs to their schedule and to backup_run.
type Agent struct {
	cfg     Config
	pool    *pgxpool.Pool
	runs    *RunStore
	metrics *Metrics
	logger  *slog.Logger
	jobs    []jobSpec

	cancel   context.CancelFunc
	stopOnce sync.Once
	wg       sync.WaitGroup
	http     *http.Server
}

// New builds the agent. store carries the external bucket; the dump job is the
// only registry entry in this PR.
func New(cfg Config, pool *pgxpool.Pool, store Uploader, logger *slog.Logger) (*Agent, error) {
	a := &Agent{
		cfg:     cfg,
		pool:    pool,
		runs:    NewRunStore(pool),
		metrics: NewMetrics(),
		logger:  logger,
	}
	dump, err := NewDumpJob(cfg, pool, store, logger)
	if err != nil {
		return nil, err
	}
	a.jobs = append(a.jobs, jobSpec{name: JobDump, anchor: cfg.DumpAt, run: dump.Run})

	if warning := dump.VersionSkewWarning(context.Background()); warning != "" {
		logger.Warn(warning)
	}
	return a, nil
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

	if _, err := a.runs.ExpireStale(ctx, a.cfg.StaleRunTTL()); err != nil {
		a.logger.Warn("boot janitor failed", "err", err)
	}
	for _, spec := range a.jobs {
		if last, err := a.runs.LastSuccess(ctx, spec.name); err == nil {
			a.metrics.SeedLastSuccess(spec.name, last)
		}
	}

	for _, spec := range a.jobs {
		if !spec.anchor.Enabled() {
			a.logger.Info("job disabled (no anchor configured)", "job", spec.name)
			continue
		}
		a.wg.Add(1)
		go a.scheduleLoop(ctx, spec)
	}
	a.wg.Add(2)
	go a.requestedLoop(ctx)
	go a.janitorLoop(ctx)

	a.serveHTTP(ctx)
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
			a.execute(ctx, spec, time.Now(), 0)
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
	artifact, meta, reason, runErr := spec.run(ctx)
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

	if err := a.runs.Succeed(recordCtx, id, artifact, meta); err != nil {
		a.logger.Error("record success", "job", spec.name, "err", err)
		return
	}
	var artifactBytes int64
	if artifact != nil {
		artifactBytes = artifact.Bytes
	}
	a.metrics.ObserveSuccess(spec.name, time.Now(), duration, artifactBytes)
	a.logger.Info("run succeeded", "job", spec.name, "run_id", id,
		"duration", duration.Round(time.Millisecond), "artifact_bytes", artifactBytes)
}

// waitReady polls the database until it answers or ctx dies.
func (a *Agent) waitReady(ctx context.Context) bool {
	for {
		pingCtx, done := context.WithTimeout(ctx, 3*time.Second)
		err := a.pool.Ping(pingCtx)
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

func (a *Agent) serveHTTP(ctx context.Context) {
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

	a.http = &http.Server{Addr: a.cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := a.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Error("metrics server", "err", err)
		}
	}()
	_ = ctx // loops carry the cancellation; the server stops via Stop()
}

package auth

import (
	"context"
	"log/slog"
	"time"
)

// Sweeper periodically deletes expired session, used-token and invite rows.
//
// Without it these tables only grow: every login leaves a session row and every
// rotation leaves a consumed-token row, and neither is ever deleted on the hot
// path — the reuse detector NEEDS consumed tokens to stick around, so a
// delete-on-rotation would defeat the whole mechanism.
type Sweeper struct {
	repo     *Repository
	logger   *slog.Logger
	interval time.Duration
	// retain is how long a dead row survives past its expiry. It is generous on
	// purpose: session_used_token is the reuse detector's memory, so pruning it
	// too eagerly turns a replay of a recently-rotated token into an ordinary
	// 401 instead of the family-killing security event it should be.
	retain time.Duration
	done   chan struct{}

	// inMemory are the process-local caches that need pruning on the same
	// schedule as the database rows. They are hooks rather than concrete types
	// so the sweeper does not have to import the handler it prunes.
	inMemory []func(time.Duration) int
}

// WithInMemory registers an in-process cache to prune on each tick.
//
// The rate-limit buckets and the last_seen_at throttle map both grow with
// traffic and are trimmed by nothing else. Hanging them off the ticker that
// already exists keeps the cleanup in one place instead of adding a goroutine
// per cache — and, more importantly, means a new cache is one line away from
// being swept rather than being forgotten the way these two were.
func (s *Sweeper) WithInMemory(fns ...func(time.Duration) int) *Sweeper {
	s.inMemory = append(s.inMemory, fns...)
	return s
}

// SweepDefaults are the production knobs: hourly, keeping dead rows a week.
const (
	DefaultSweepInterval = time.Hour
	DefaultSweepRetain   = 7 * 24 * time.Hour
)

func NewSweeper(repo *Repository, logger *slog.Logger, interval, retain time.Duration) *Sweeper {
	if interval <= 0 {
		interval = DefaultSweepInterval
	}
	if retain <= 0 {
		retain = DefaultSweepRetain
	}
	return &Sweeper{repo: repo, logger: logger, interval: interval, retain: retain, done: make(chan struct{})}
}

// Start runs the sweep loop until ctx is cancelled.
//
// The first sweep is deferred by one interval rather than run at boot: startup
// is already the busiest moment for the database (migrations, pool warm-up,
// requeuePending), and a DELETE across three tables adds nothing urgent to it.
func (s *Sweeper) Start(ctx context.Context) {
	go func() {
		defer close(s.done)
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.sweepOnce(ctx)
			}
		}
	}()
}

// Wait blocks until the loop has exited. Used by main's graceful shutdown and
// by tests that need the goroutine gone before the pool closes.
func (s *Sweeper) Wait() { <-s.done }

func (s *Sweeper) sweepOnce(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	n, err := s.repo.Sweep(ctx, s.retain)
	if err != nil {
		s.logger.Error("session sweep", "err", err)
		// Deliberately falls through: a database error must not stop the
		// in-memory caches from being pruned, since those grow independently
		// of whether the DELETE succeeded.
	}

	// Challenges, e-mail OTPs and reset tokens are all written on
	// UNAUTHENTICATED paths, so they accumulate at whatever rate an attacker
	// chooses. Sweeping them is not housekeeping — it is the only thing
	// bounding three tables anyone on the network can insert into.
	tf, err := s.repo.SweepTwoFactor(ctx, s.retain)
	if err != nil {
		s.logger.Error("two-factor sweep", "err", err)
	}
	n += tf

	evicted := 0
	for _, prune := range s.inMemory {
		evicted += prune(s.memoryRetain())
	}
	if n > 0 || evicted > 0 {
		s.logger.Info("sweep", "rows_deleted", n, "memory_keys_evicted", evicted)
	}
}

// memoryRetain is how long an idle in-memory key survives.
//
// Much shorter than the row retention: these caches hold no durable state
// worth keeping, and dropping a live entry early is harmless — a rate-limit
// bucket re-seeds on the next attempt, and a throttle entry costs one extra
// last_seen_at UPDATE. A live LOCKOUT is never dropped; attemptlimit.Sweep
// skips those explicitly.
func (s *Sweeper) memoryRetain() time.Duration {
	return 2 * time.Hour
}

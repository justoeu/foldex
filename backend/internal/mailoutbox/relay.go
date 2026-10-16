package mailoutbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"foldex/internal/mailer"
	"foldex/internal/pkg/secrets"
)

const (
	DefaultPollInterval = time.Second
	DefaultBatch        = 32
	DefaultWorkers      = 2
	DefaultMaxAttempts  = 6
	DefaultSendTimeout  = 15 * time.Second
	// A claim older than this belonged to a relay that died. It is comfortably
	// longer than the send timeout so a slow-but-alive send is never stolen.
	DefaultClaimTTL      = 2 * time.Minute
	DefaultPublishedTTL  = 7 * 24 * time.Hour
	DefaultFailedTTL     = 90 * 24 * time.Hour
	defaultPurgeInterval = time.Hour
)

// ErrPermanent marks a failure that retrying cannot fix — a template that no
// longer exists, a payload that will not decrypt. Retrying those six times just
// delays the operator finding out.
var ErrPermanent = errors.New("mailoutbox: permanent failure")

// Sink is where a claimed message goes. The inproc sink renders and sends it;
// an AMQP sink forwards the still-encrypted params to a broker, which is why
// Deliver receives the row rather than a rendered Message.
type Sink interface {
	Deliver(ctx context.Context, msg Outgoing) error
	Name() string
}

type Options struct {
	PollInterval time.Duration
	Batch        int
	Workers      int
	MaxAttempts  int
	SendTimeout  time.Duration
	ClaimTTL     time.Duration
	PublishedTTL time.Duration
	FailedTTL    time.Duration
}

type queue interface {
	Claim(context.Context, int) ([]Outgoing, error)
	MarkPublished(context.Context, int64, string) error
	MarkFailed(context.Context, int64, string, string, time.Duration, int, bool) error
	RequeueStuck(context.Context, time.Duration) (int64, error)
	Purge(context.Context, time.Duration, time.Duration) (int64, error)
}

// Relay drains the outbox into a Sink.
//
// It is the only component that knows both the queue and the transport, which
// is what keeps the transport pluggable: the handlers write rows and know
// nothing about brokers, and the sink delivers messages and knows nothing about
// Postgres.
type Relay struct {
	repo   queue
	sink   Sink
	logger *slog.Logger
	opts   Options

	life Lifecycle
}

func NewRelay(repo *Repository, sink Sink, opts Options, logger *slog.Logger) *Relay {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.Batch <= 0 {
		opts.Batch = DefaultBatch
	}
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultMaxAttempts
	}
	if opts.SendTimeout <= 0 {
		opts.SendTimeout = DefaultSendTimeout
	}
	if opts.ClaimTTL <= 0 {
		opts.ClaimTTL = DefaultClaimTTL
	}
	if opts.PublishedTTL <= 0 {
		opts.PublishedTTL = DefaultPublishedTTL
	}
	if opts.FailedTTL <= 0 {
		opts.FailedTTL = DefaultFailedTTL
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Relay{repo: repo, sink: sink, logger: logger, opts: opts}
}

// Start launches the polling loop. It returns immediately.
func (rl *Relay) Start(parent context.Context) {
	rl.life.Start(parent, func(context.Context) { rl.loop() })
}

func (rl *Relay) Stop() { rl.life.Stop() }

func (rl *Relay) loop() {
	ticker := time.NewTicker(rl.opts.PollInterval)
	defer ticker.Stop()
	purge := time.NewTicker(defaultPurgeInterval)
	defer purge.Stop()

	// One sweep at boot. A relay that was killed mid-send left rows in flight,
	// and waiting a full claim TTL to notice would delay a sign-in code by
	// minutes for no reason.
	rl.requeueStuck()

	for {
		select {
		case <-rl.life.Ctx().Done():
			return
		case <-purge.C:
			rl.purge()
			rl.requeueStuck()
		case <-ticker.C:
			for rl.drain() {
				// A full batch means more is waiting; keep going rather than
				// idling until the next tick with a backlog on the floor.
				if rl.life.Stopped() || rl.life.Ctx().Err() != nil {
					return
				}
			}
		}
	}
}

// drain claims one batch and delivers it. It reports whether the batch was
// full, which is the caller's signal that more work is queued.
func (rl *Relay) drain() bool {
	msgs, ok := rl.claimBatch()
	if !ok {
		return false
	}
	jobs := make(chan Outgoing)
	var wg sync.WaitGroup
	rl.startDrainWorkers(jobs, &wg, len(msgs))
	full := rl.dispatchBatch(jobs, &wg, msgs)
	return full
}

func (rl *Relay) claimBatch() ([]Outgoing, bool) {
	if rl.life.Stopped() || rl.life.Ctx().Err() != nil {
		return nil, false
	}
	msgs, err := rl.repo.Claim(rl.life.Ctx(), rl.opts.Batch)
	if err != nil {
		if rl.life.Ctx().Err() == nil {
			rl.logger.Error("mail outbox claim", "err", err)
		}
		return nil, false
	}
	if len(msgs) == 0 {
		return nil, false
	}
	return msgs, true
}

func (rl *Relay) startDrainWorkers(jobs <-chan Outgoing, wg *sync.WaitGroup, n int) {
	for range min(rl.opts.Workers, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range jobs {
				rl.deliverGuarded(m)
			}
		}()
	}
}

func (rl *Relay) deliverGuarded(m Outgoing) {
	defer func() {
		if r := recover(); r != nil {
			rl.logger.Error("mail delivery panicked", "id", m.ID, "panic", r)
		}
	}()
	rl.deliver(m)
}

func (rl *Relay) dispatchBatch(jobs chan Outgoing, wg *sync.WaitGroup, msgs []Outgoing) bool {
	for _, m := range msgs {
		select {
		case <-rl.life.Ctx().Done():
			close(jobs)
			wg.Wait()
			return false
		case jobs <- m:
		}
	}
	close(jobs)
	wg.Wait()
	return len(msgs) == rl.opts.Batch
}

// errInvalidClaim marks a claimed row that cannot be settled: without an id
// and a claim token the CAS settle statements can never match it, so running
// it through the sink would send mail whose outcome is unrecordable.
var errInvalidClaim = errors.New("mailoutbox: invalid claim")

// validateClaim is the pure check that a claimed row is settleable at all.
// It reads no state and cannot fail for a row Claim actually produced — the
// columns it checks are the ones Claim itself writes.
func validateClaim(m Outgoing) error {
	switch {
	case m.ID <= 0:
		return fmt.Errorf("%w: missing id", errInvalidClaim)
	case m.ClaimToken == "":
		return fmt.Errorf("%w: missing claim token", errInvalidClaim)
	default:
		return nil
	}
}

// claimStage names how a finished delivery is settled.
type claimStage int

const (
	stageSettledPublished claimStage = iota
	stageSettledRetry
	stageSettledPermanent
)

// claimRecovery maps a delivery outcome to its settle stage, in order. The
// table keeps the decision in one place: deliver runs the claim, then the
// first matching row says what happens to it.
var claimRecovery = []struct {
	match func(error) bool
	stage claimStage
}{
	{func(err error) bool { return err == nil }, stageSettledPublished},
	{func(err error) bool { return errors.Is(err, ErrPermanent) }, stageSettledPermanent},
	{func(error) bool { return true }, stageSettledRetry},
}

func claimStageFor(err error) claimStage {
	for _, row := range claimRecovery {
		if row.match(err) {
			return row.stage
		}
	}
	return stageSettledRetry
}

func (rl *Relay) deliver(m Outgoing) {
	if err := validateClaim(m); err != nil {
		rl.logger.Error("mail outbox invalid claim", "id", m.ID, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(rl.life.Ctx(), rl.opts.SendTimeout)
	err := rl.sink.Deliver(ctx, m)
	cancel()
	rl.recoverClaim(m, err)
}

// recoverClaim settles one delivered claim according to claimRecovery and
// reports the stage it landed in. Settling uses a context detached from
// the relay's: a send that succeeded and then could not be recorded is
// redelivered on the next claim, and during shutdown that is exactly when
// the cancellation lands.
func (rl *Relay) recoverClaim(m Outgoing, deliverErr error) (claimStage, error) {
	stage := claimStageFor(deliverErr)
	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(rl.life.Ctx()), 5*time.Second)
	defer settleCancel()

	if stage == stageSettledPublished {
		if merr := rl.repo.MarkPublished(settleCtx, m.ID, m.ClaimToken); merr != nil {
			rl.logger.Error("mail outbox settle", "id", m.ID, "err", merr)
			return stage, merr
		}
		return stage, nil
	}

	reason := failureReason(deliverErr)
	fatal := stage == stageSettledPermanent
	retryIn := backoff(m.Attempts)
	rl.logger.Warn("mail delivery failed",
		"template", m.Template, "attempt", m.Attempts, "reason", reason,
		"permanent", fatal, "retry_in", retryIn.String(), "sink", rl.sink.Name())
	if merr := rl.repo.MarkFailed(settleCtx, m.ID, m.ClaimToken, reason,
		retryIn, rl.opts.MaxAttempts, fatal); merr != nil {
		rl.logger.Error("mail outbox settle failure", "id", m.ID, "err", merr)
		return stage, merr
	}
	return stage, nil
}

func (rl *Relay) requeueStuck() {
	ctx, cancel := context.WithTimeout(rl.life.Ctx(), 10*time.Second)
	defer cancel()
	n, err := rl.repo.RequeueStuck(ctx, rl.opts.ClaimTTL)
	if err != nil {
		if rl.life.Ctx().Err() == nil {
			rl.logger.Error("mail outbox requeue", "err", err)
		}
		return
	}
	if n > 0 {
		rl.logger.Warn("mail outbox reclaimed abandoned messages", "count", n)
	}
}

func (rl *Relay) purge() {
	ctx, cancel := context.WithTimeout(rl.life.Ctx(), 30*time.Second)
	defer cancel()
	if _, err := rl.repo.Purge(ctx, rl.opts.PublishedTTL, rl.opts.FailedTTL); err != nil && rl.life.Ctx().Err() == nil {
		rl.logger.Error("mail outbox purge", "err", err)
	}
}

// backoff spaces retries out. The first retry is quick because the common
// failure is a transient blip and the message is usually a sign-in code someone
// is waiting for; later ones spread out because a provider that has refused
// four times is not going to accept the fifth a minute later.
func backoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return time.Minute
	case attempt == 2:
		return 5 * time.Minute
	case attempt == 3:
		return 15 * time.Minute
	case attempt == 4:
		return 30 * time.Minute
	default:
		return time.Hour
	}
}

// failureReason collapses an error into a stable token for the row and the log.
//
// The transport's own text never reaches either: an SMTP rejection routinely
// quotes the envelope back, and the envelope here is a recipient address.
func failureReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, mailer.ErrUnknownTemplate):
		return "unknown_template"
	case errors.Is(err, secrets.ErrDecrypt):
		return "undecryptable_payload"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	return "send_failed"
}

// permanent wraps err so the relay stops retrying it.
func permanent(err error) error { return fmt.Errorf("%w: %w", ErrPermanent, err) }

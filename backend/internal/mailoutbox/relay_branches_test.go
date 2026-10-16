package mailoutbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// branchQueue scripts every Relay repository outcome the purge suite does
// not: claim batches/errors and both settle verbs.
type branchQueue struct {
	mu             sync.Mutex
	claimed        []Outgoing
	claimErr       error
	claimCalls     int
	published      []int64
	publishErr     error
	failed         []failedMark
	markFailedErr  error
	requeueN       int64
	requeueErr     error
	requeueCalls   int
	requeueClaimed time.Duration
}

type failedMark struct {
	id     int64
	reason string
	retry  time.Duration
	max    int
	fatal  bool
}

func (q *branchQueue) Claim(_ context.Context, n int) ([]Outgoing, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claimCalls++
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if n > len(q.claimed) {
		n = len(q.claimed)
	}
	out := append([]Outgoing(nil), q.claimed[:n]...)
	q.claimed = q.claimed[n:]
	return out, nil
}

func (q *branchQueue) MarkPublished(_ context.Context, id int64, _ string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.published = append(q.published, id)
	return q.publishErr
}

func (q *branchQueue) MarkFailed(_ context.Context, id int64, _ string, reason string, retry time.Duration, max int, fatal bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failed = append(q.failed, failedMark{id: id, reason: reason, retry: retry, max: max, fatal: fatal})
	return q.markFailedErr
}

func (q *branchQueue) RequeueStuck(_ context.Context, ttl time.Duration) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.requeueCalls++
	q.requeueClaimed = ttl
	return q.requeueN, q.requeueErr
}

func (*branchQueue) Purge(context.Context, time.Duration, time.Duration) (int64, error) {
	return 0, nil
}

type branchSink struct {
	mu  sync.Mutex
	err error
	got []int64
}

func (*branchSink) Name() string { return "branch-sink" }
func (s *branchSink) Deliver(_ context.Context, m Outgoing) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, m.ID)
	return s.err
}

func branchRelay(t *testing.T, q *branchQueue, sink Sink, parent context.Context) (*Relay, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	rl := NewRelay(nil, sink, Options{
		Batch: 2, Workers: 1, MaxAttempts: 4,
		PollInterval: 5 * time.Millisecond, ClaimTTL: 47 * time.Second,
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	rl.repo = q
	rl.life.SeedContext(parent)
	t.Cleanup(func() { rl.life.Stop() })
	return rl, &logs
}

func TestNewRelay_ZeroOptionsTakeEveryDefault(t *testing.T) {
	rl := NewRelay(nil, nil, Options{}, nil)
	assert.Equal(t, DefaultPollInterval, rl.opts.PollInterval)
	assert.Equal(t, DefaultBatch, rl.opts.Batch)
	assert.Equal(t, DefaultWorkers, rl.opts.Workers)
	assert.Equal(t, DefaultMaxAttempts, rl.opts.MaxAttempts)
	assert.Equal(t, DefaultSendTimeout, rl.opts.SendTimeout)
	assert.Equal(t, DefaultClaimTTL, rl.opts.ClaimTTL)
	assert.Equal(t, DefaultPublishedTTL, rl.opts.PublishedTTL)
	assert.Equal(t, DefaultFailedTTL, rl.opts.FailedTTL)
	assert.NotNil(t, rl.logger, "a nil logger falls back to slog.Default")
}

func TestValidateClaimAndStageMapping(t *testing.T) {
	assert.ErrorIs(t, validateClaim(Outgoing{}), errInvalidClaim)
	assert.Contains(t, validateClaim(Outgoing{}).Error(), "missing id")
	assert.Contains(t, validateClaim(Outgoing{ID: 1}).Error(), "missing claim token")
	require.NoError(t, validateClaim(Outgoing{ID: 1, ClaimToken: "tok"}))

	assert.Equal(t, stageSettledPublished, claimStageFor(nil))
	assert.Equal(t, stageSettledPermanent, claimStageFor(fmt.Errorf("wrap: %w", ErrPermanent)))
	assert.Equal(t, stageSettledRetry, claimStageFor(errors.New("smtp down")))
}

func TestDeliver_InvalidClaimNeverReachesTheSink(t *testing.T) {
	sink := &branchSink{}
	rl, logs := branchRelay(t, &branchQueue{}, sink, context.Background())

	rl.deliver(Outgoing{ID: 0, ClaimToken: ""})
	rl.deliver(Outgoing{ID: 5, ClaimToken: ""})

	assert.Empty(t, sink.got, "an unsettleable row must not send mail whose outcome is unrecordable")
	assert.Contains(t, logs.String(), "invalid claim")
}

func TestDeliver_SettlesPublishedRetryAndPermanent(t *testing.T) {
	msg := Outgoing{ID: 7, ClaimToken: "tok", Template: "invite", Attempts: 2}

	t.Run("success publishes", func(t *testing.T) {
		q := &branchQueue{}
		rl, _ := branchRelay(t, q, &branchSink{}, context.Background())
		rl.deliver(msg)
		assert.Equal(t, []int64{7}, q.published)
		assert.Empty(t, q.failed)
	})

	t.Run("transient failure schedules a retry", func(t *testing.T) {
		q := &branchQueue{}
		rl, logs := branchRelay(t, q, &branchSink{err: errors.New("smtp 421")}, context.Background())
		rl.deliver(msg)
		require.Len(t, q.failed, 1)
		assert.Equal(t, failedMark{id: 7, reason: "send_failed", retry: 5 * time.Minute, max: 4, fatal: false}, q.failed[0])
		assert.Empty(t, q.published)
		assert.Contains(t, logs.String(), "mail delivery failed")
	})

	t.Run("permanent failure is fatal", func(t *testing.T) {
		q := &branchQueue{}
		rl, _ := branchRelay(t, q, &branchSink{err: permanent(errors.New("no template"))}, context.Background())
		rl.deliver(msg)
		require.Len(t, q.failed, 1)
		assert.True(t, q.failed[0].fatal)
	})
}

func TestRecoverClaim_SettleErrorsAreReturnedAndLogged(t *testing.T) {
	msg := Outgoing{ID: 3, ClaimToken: "tok"}
	boom := errors.New("settle exploded")

	q := &branchQueue{publishErr: boom}
	rl, logs := branchRelay(t, q, &branchSink{}, context.Background())
	stage, err := rl.recoverClaim(msg, nil)
	assert.Equal(t, stageSettledPublished, stage)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, logs.String(), "mail outbox settle")

	q = &branchQueue{markFailedErr: boom}
	rl, logs = branchRelay(t, q, &branchSink{}, context.Background())
	stage, err = rl.recoverClaim(msg, errors.New("send blew up"))
	assert.Equal(t, stageSettledRetry, stage)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, logs.String(), "mail outbox settle failure")
}

func TestClaimBatch_ErrorEmptyAndStopped(t *testing.T) {
	boom := errors.New("claim exploded")

	q := &branchQueue{claimErr: boom}
	rl, logs := branchRelay(t, q, &branchSink{}, context.Background())
	msgs, ok := rl.claimBatch()
	assert.False(t, ok)
	assert.Nil(t, msgs)
	assert.Contains(t, logs.String(), "mail outbox claim")

	q = &branchQueue{}
	rl, _ = branchRelay(t, q, &branchSink{}, context.Background())
	_, ok = rl.claimBatch()
	assert.False(t, ok, "an empty claim is not a batch")

	// Same failure during shutdown is expected noise and stays quiet.
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	q = &branchQueue{claimErr: boom}
	rl, logs = branchRelay(t, q, &branchSink{}, parent)
	_, ok = rl.claimBatch()
	assert.False(t, ok)
	assert.NotContains(t, logs.String(), "mail outbox claim")
}

func TestDrain_FullBatchReportsMoreWork(t *testing.T) {
	q := &branchQueue{claimed: []Outgoing{
		{ID: 1, ClaimToken: "t1"}, {ID: 2, ClaimToken: "t2"}, {ID: 3, ClaimToken: "t3"},
	}}
	rl, _ := branchRelay(t, q, &branchSink{}, context.Background()) // Batch=2

	assert.True(t, rl.drain(), "a batch filled to Batch means more is queued")
	assert.False(t, rl.drain(), "a short batch drains the queue")
	assert.ElementsMatch(t, []int64{1, 2, 3}, q.published)
}

func TestDispatchBatch_AbortsWhenTheRelayStopsMidBatch(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	rl, _ := branchRelay(t, &branchQueue{}, &branchSink{}, parent)
	cancel() // lifecycle context done before the first job is dispatched

	jobs := make(chan Outgoing) // no workers: only the Done arm can fire
	var wg sync.WaitGroup
	full := rl.dispatchBatch(jobs, &wg, []Outgoing{{ID: 1, ClaimToken: "t"}})
	assert.False(t, full, "a stopped relay reports 'no more' so the loop exits")
}

func TestRequeueStuck_LogsReclaimErrorsButStaysQuietOnShutdown(t *testing.T) {
	q := &branchQueue{requeueN: 5}
	rl, logs := branchRelay(t, q, &branchSink{}, context.Background())
	rl.requeueStuck()
	assert.Equal(t, 47*time.Second, q.requeueClaimed, "the configured claim TTL is the requeue horizon")
	assert.Contains(t, logs.String(), "reclaimed abandoned messages")

	q = &branchQueue{requeueErr: errors.New("requeue exploded")}
	rl, logs = branchRelay(t, q, &branchSink{}, context.Background())
	rl.requeueStuck()
	assert.Contains(t, logs.String(), "mail outbox requeue")

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	q = &branchQueue{requeueErr: errors.New("requeue exploded")}
	rl, logs = branchRelay(t, q, &branchSink{}, parent)
	rl.requeueStuck()
	assert.NotContains(t, logs.String(), "mail outbox requeue")
}

func TestRelayStartStop_RequeuesAtBootDrainsAndStops(t *testing.T) {
	q := &branchQueue{claimed: []Outgoing{{ID: 9, ClaimToken: "tok-9"}}}
	sink := &branchSink{}
	var logs bytes.Buffer
	rl := NewRelay(nil, sink, Options{
		Batch: 4, Workers: 1, PollInterval: 5 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	rl.repo = q

	rl.Start(context.Background())
	require.Eventually(t, func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.published) == 1
	}, 2*time.Second, time.Millisecond, "the loop must claim and settle the queued message")
	assert.Positive(t, q.requeueCalls, "boot sweeps abandoned claims before the first tick")

	done := make(chan struct{})
	go func() {
		rl.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not join the loop")
	}
	assert.False(t, rl.drain(), "a stopped relay claims nothing")
}

type scriptedNetError struct{ timeout bool }

func (e scriptedNetError) Error() string   { return "scripted network failure" }
func (e scriptedNetError) Timeout() bool   { return e.timeout }
func (e scriptedNetError) Temporary() bool { return false }

var _ net.Error = scriptedNetError{}

func TestFailureReason_NetworkShapes(t *testing.T) {
	assert.Equal(t, "timeout", failureReason(fmt.Errorf("dial: %w", scriptedNetError{timeout: true})))
	assert.Equal(t, "network", failureReason(scriptedNetError{}))
	// A DNS failure IS a net.Error, so the network arm claims it first; the
	// dedicated dns token is for shapes errors.As unwraps differently. Pin
	// the observed precedence rather than the assumed one.
	dns := &net.DNSError{Err: "no such host", Name: "smtp.invalid"}
	assert.Equal(t, "network", failureReason(dns))
}

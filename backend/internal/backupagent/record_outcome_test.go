package backupagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAgent is the smallest Agent recordOutcome needs: a logger and a budget
// short enough that the exhaustion path finishes inside a test.
func testAgent(budget, gap time.Duration) *Agent {
	return &Agent{
		logger:        testLogger(),
		recordBudget:  budget,
		recordGap:     gap,
		recordAttempt: time.Second,
	}
}

// The whole point of the retry — see recordOutcome's doc comment for the
// incident that produced it.

func TestRecordOutcome_ATransientFailureDoesNotLoseTheOutcome(t *testing.T) {
	a := testAgent(2*time.Second, 10*time.Millisecond)
	attempts := 0

	ok := a.recordOutcome("fail", "drill", func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("context deadline exceeded")
		}
		return nil
	})

	assert.True(t, ok)
	assert.Equal(t, 3, attempts, "it keeps trying until the write lands")
}

func TestRecordOutcome_SucceedsOnTheFirstTryWithoutSleeping(t *testing.T) {
	a := testAgent(time.Minute, time.Hour) // a gap that would hang if it slept
	attempts := 0

	start := time.Now()
	ok := a.recordOutcome("succeed", "dump", func(context.Context) error {
		attempts++
		return nil
	})

	assert.True(t, ok)
	assert.Equal(t, 1, attempts)
	assert.Less(t, time.Since(start), time.Second, "a write that lands must not pay the retry gap")
}

/*
 * The budget has to END. An unbounded retry would be worse than the bug it
 * fixes: the agent's job loop would never return, and a database that is down
 * for maintenance would wedge the process instead of leaving one bad row for
 * the janitor.
 */
func TestRecordOutcome_GivesUpWhenTheBudgetIsSpent(t *testing.T) {
	a := testAgent(120*time.Millisecond, 20*time.Millisecond)
	attempts := 0

	start := time.Now()
	ok := a.recordOutcome("fail", "drill", func(context.Context) error {
		attempts++
		return errors.New("connection refused")
	})
	elapsed := time.Since(start)

	assert.False(t, ok, "the caller must learn the row was left inconsistent")
	assert.Greater(t, attempts, 1, "giving up on the first error is the bug, not the fix")
	assert.Less(t, elapsed, 2*time.Second, "the budget bounds the wait")
}

/*
 * Each attempt gets a FRESH context built from Background, never the run's.
 * The run's ctx is exactly what shutdown cancels, and an outcome that goes
 * unrecorded because the process is stopping is the stale row this exists to
 * prevent — so a cancelled world must not stop the write from being attempted.
 */
func TestRecordOutcome_WritesEvenWhileTheProcessIsShuttingDown(t *testing.T) {
	a := testAgent(time.Second, 10*time.Millisecond)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	// Asserted INSIDE the callback: recordOutcome cancels each attempt's
	// context as soon as the write returns, so a ctx captured and inspected
	// afterwards is always cancelled and would prove nothing.
	var live, hasDeadline bool
	var deadline time.Time
	ok := a.recordOutcome("fail", "dump", func(c context.Context) error {
		live = c.Err() == nil
		deadline, hasDeadline = c.Deadline()
		return nil
	})

	require.True(t, ok)
	assert.True(t, live, "the write context must not inherit a cancelled parent")
	assert.Error(t, cancelled.Err(), "sanity: the surrounding world really is cancelled")
	assert.True(t, hasDeadline, "an attempt without a deadline can hang the job loop forever")
	assert.WithinDuration(t, time.Now().Add(a.recordAttempt), deadline, time.Second)
}

/*
 * An Agent assembled by hand — which every integration test in this package
 * does — must retry like production.
 *
 * The first cut of the seam read the fields raw, so a zero recordAttempt built
 * an already-expired context and the write could never land: two existing
 * integration tests went red on a run that recorded no outcome at all.
 */
func TestRecordOutcome_AZeroBudgetMeansTheCompiledDefault(t *testing.T) {
	a := &Agent{logger: testLogger()} // no budgets set at all
	attempts := 0

	var perAttempt time.Duration
	ok := a.recordOutcome("fail", "dump", func(c context.Context) error {
		attempts++
		deadline, has := c.Deadline()
		require.True(t, has)
		perAttempt = time.Until(deadline)
		if attempts < 2 {
			return errors.New("connection refused")
		}
		return nil
	})

	assert.True(t, ok, "a zero field must not mean a zero timeout")
	assert.Equal(t, 2, attempts, "the retry has to survive an Agent built without a constructor")
	assert.WithinDuration(t, time.Now().Add(recordAttemptTimeout), time.Now().Add(perAttempt), time.Second)
}

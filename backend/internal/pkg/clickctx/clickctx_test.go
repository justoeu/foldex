package clickctx

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The default has to be "record". Every caller of Allow is a repository on a
// path that has always written a click row, and a context without a gate is the
// ordinary state for all of them — a background job, an import, a test, a
// public route mounted somewhere the middleware is not. Defaulting to "suppress"
// would silently stop counting clicks the day someone mounted the route
// elsewhere, and a counter that reads zero looks like a quiet day.
func TestAllow_WithoutAGateRecords(t *testing.T) {
	t.Parallel()
	assert.True(t, Allow(context.Background(), "link", 42))
}

func TestAllow_ConsultsTheInstalledGate(t *testing.T) {
	t.Parallel()
	var seenKind string
	var seenID int64
	ctx := WithGate(context.Background(), func(kind string, id int64) bool {
		seenKind, seenID = kind, id
		return false
	})

	assert.False(t, Allow(ctx, "note", 7))
	assert.Equal(t, "note", seenKind)
	assert.EqualValues(t, 7, seenID)
}

// A nil gate is the shape a caller lands on when a policy turned coalescing
// off. It must mean "record", not panic on the public /go path — which is
// reached by an anonymous visitor, where a panic is a 500 on a share link.
func TestWithGate_NilGateRecordsAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	ctx := WithGate(context.Background(), nil)
	assert.NotPanics(t, func() { assert.True(t, Allow(ctx, "link", 1)) })
}

// The gate is called from inside a repository transaction and the same context
// may be shared by concurrent work. Run with -race.
func TestAllow_IsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	calls := 0
	ctx := WithGate(context.Background(), func(string, int64) bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return true
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Allow(ctx, "link", 1)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 50, calls)
}

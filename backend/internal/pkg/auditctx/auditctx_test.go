package auditctx

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Annotation is optional BY CONSTRUCTION: a handler outside a request that
// carries a holder must not panic, and must not silently create one nobody
// reads. Set is called from four CRUD packages, and a nil-holder panic there
// would take down an ordinary content write.
func TestSet_WithoutAHolderIsANoOp(t *testing.T) {
	ctx := context.Background()
	assert.NotPanics(t, func() { Set(ctx, "link", 1, "title") })
	kind, id, subject := Get(ctx)
	assert.Empty(t, kind)
	assert.Nil(t, id)
	assert.Empty(t, subject)
}

func TestSetAndGet_RoundTrip(t *testing.T) {
	ctx := With(context.Background())
	Set(ctx, "link", 42, "ADR-46 draft")

	kind, id, subject := Get(ctx)
	assert.Equal(t, "link", kind)
	assert.Equal(t, "ADR-46 draft", subject)
	if assert.NotNil(t, id) {
		assert.Equal(t, int64(42), *id)
	}
}

// Zero is not an id. A handler that annotates before it knows the row — or one
// whose repository returned a zero value — must leave the column NULL rather
// than claim row 0, which no table has.
func TestSet_ZeroIdLeavesTheReferenceAbsent(t *testing.T) {
	ctx := With(context.Background())
	Set(ctx, "folder", 0, "Inbox")

	kind, id, subject := Get(ctx)
	assert.Equal(t, "folder", kind)
	assert.Equal(t, "Inbox", subject)
	assert.Nil(t, id, "id 0 must not be recorded as a row reference")
}

// The last annotation wins: a handler that learns more as it goes should be
// able to say so, and the alternative — first-write-wins — would silently keep
// a placeholder.
func TestSet_LastAnnotationWins(t *testing.T) {
	ctx := With(context.Background())
	Set(ctx, "note", 1, "draft")
	Set(ctx, "note", 2, "final")

	kind, id, subject := Get(ctx)
	assert.Equal(t, "note", kind)
	assert.Equal(t, "final", subject)
	if assert.NotNil(t, id) {
		assert.Equal(t, int64(2), *id)
	}
}

func TestSetRequest_AnnotatesTheRequestsContext(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/links", nil)
	r = r.WithContext(With(r.Context()))
	SetRequest(r, "tag", 7, "reading")

	kind, id, _ := Get(r.Context())
	assert.Equal(t, "tag", kind)
	if assert.NotNil(t, id) {
		assert.Equal(t, int64(7), *id)
	}
}

// The middleware reads the holder AFTER the handler returns, and a handler is
// free to spawn work. That is the race the mutex exists for, and a race the
// detector would otherwise find long after this shipped.
func TestSet_IsSafeUnderConcurrentAnnotationAndRead(t *testing.T) {
	ctx := With(context.Background())
	var wg sync.WaitGroup
	for i := 1; i <= 40; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			Set(ctx, "link", int64(n), "title")
			Get(ctx)
		}(i)
	}
	wg.Wait()
}

// A context that carries something else under a different key must not be
// mistaken for a holder.
func TestGet_IgnoresAForeignValue(t *testing.T) {
	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, "not a holder")
	kind, id, subject := Get(ctx)
	assert.Empty(t, kind)
	assert.Nil(t, id)
	assert.Empty(t, subject)
}

// The flag says whether a CONFIGURED proxy vouched for the address. False by
// default is what makes an unmarked request honest on a direct bind — the
// product's own default.
func TestIPTrusted_DefaultsToFalse(t *testing.T) {
	assert.False(t, IPTrusted(context.Background()))
	assert.True(t, IPTrusted(MarkTrustedIP(context.Background())))
}

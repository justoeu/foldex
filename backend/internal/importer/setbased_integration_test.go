//go:build integration

package importer

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/preview"
	"foldex/internal/testdb"
)

type roundTripCounter struct {
	count atomic.Int64
}

func (c *roundTripCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.count.Add(1)
	return ctx
}

func (*roundTripCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *roundTripCounter) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromStartData) context.Context {
	c.count.Add(1)
	return ctx
}

func (*roundTripCounter) TraceCopyFromEnd(context.Context, *pgx.Conn, pgx.TraceCopyFromEndData) {}

func (c *roundTripCounter) reset() { c.count.Store(0) }
func (c *roundTripCounter) total() int64 {
	return c.count.Load()
}

func tracedPool(t *testing.T, base *pgxpool.Pool, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	cfg := base.Config()
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func importRoundTripFixture(prefix string, count int) []Item {
	folder := prefix + " folder"
	items := make([]Item, count)
	for i := range items {
		items[i] = Item{
			URL:        fmt.Sprintf("https://%s.example/%d", prefix, i),
			Title:      fmt.Sprintf("%s link %d", prefix, i),
			Tags:       []string{prefix + " tag"},
			Folder:     &folder,
			ClickCount: 1,
		}
	}
	return items
}

func runCountedImport(t *testing.T, h *Handler, counter *roundTripCounter, uid authctx.UserID, prefix string, count int, mode importMode) int64 {
	t.Helper()
	counter.reset()
	imported, skipped, wiped, warnings, err := h.importItemsWithMode(
		context.Background(), uid, importRoundTripFixture(prefix, count), mode, nil,
	)
	require.NoError(t, err)
	assert.Equal(t, count, imported)
	assert.Zero(t, skipped)
	if mode == modeWipe {
		assert.Equal(t, count, wiped)
	} else {
		assert.Zero(t, wiped)
	}
	assert.Empty(t, warnings)
	return counter.total()
}

func TestImportRoundTripsStayBoundedAsInputGrows(t *testing.T) {
	base := testdb.Shared(t)
	smallUID := testdb.SeedUser(t, base, "roundtrips-small@test.local", "admin")
	largeUID := testdb.SeedUser(t, base, "roundtrips-large@test.local", "admin")

	counter := &roundTripCounter{}
	h := NewHandler(tracedPool(t, base, counter), nil)
	small := runCountedImport(t, h, counter, smallUID, "small-roundtrips", 4, modeSkip)
	large := runCountedImport(t, h, counter, largeUID, "large-roundtrips", 200, modeSkip)
	smallWipe := runCountedImport(t, h, counter, smallUID, "small-roundtrips", 4, modeWipe)
	largeWipe := runCountedImport(t, h, counter, largeUID, "large-roundtrips", 200, modeWipe)
	t.Logf("pgx round trips: skip 4=%d skip 200=%d wipe 4=%d wipe 200=%d", small, large, smallWipe, largeWipe)

	assert.LessOrEqual(t, small, int64(24), "small import already exceeds the fixed round-trip budget")
	assert.LessOrEqual(t, large, small+2,
		"database round trips must stay constant as import rows grow: small=%d large=%d", small, large)
	assert.LessOrEqual(t, smallWipe, int64(26), "small wipe already exceeds the fixed round-trip budget")
	assert.LessOrEqual(t, largeWipe, smallWipe+2,
		"wipe round trips must stay constant as import rows grow: small=%d large=%d", smallWipe, largeWipe)
}

type commitObservingEnqueuer struct {
	pool    *pgxpool.Pool
	visible []bool
}

func (e *commitObservingEnqueuer) Enqueue(id int64) error {
	var exists bool
	err := e.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM link WHERE id = $1)`, id).Scan(&exists)
	e.visible = append(e.visible, err == nil && exists)
	return err
}

func TestStagedImport_EnqueuesOnlyAfterCommit(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "enqueue-after-commit@test.local", "admin")
	enqueuer := &commitObservingEnqueuer{pool: pool}
	h := NewHandler(pool, enqueuer)

	imported, skipped, wiped, warnings, err := h.importItemsWithMode(context.Background(), uid, []Item{{
		URL: "https://enqueue-after-commit.example", Title: "Committed",
	}}, modeSkip, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Zero(t, skipped)
	assert.Zero(t, wiped)
	assert.Empty(t, warnings)
	assert.Equal(t, []bool{true}, enqueuer.visible)

	_, _, _, _, err = h.importItemsWithMode(context.Background(), uid, []Item{{
		URL: "https://enqueue-after-commit.example", Title: "Duplicate",
	}}, modeSkip, nil)
	require.NoError(t, err)
	assert.Len(t, enqueuer.visible, 1, "skipped URLs must not enqueue preview work")
}

type queueFullEnqueuer struct {
	calls  int
	fullAt int
}

func (e *queueFullEnqueuer) Enqueue(int64) error {
	e.calls++
	if e.calls == e.fullAt {
		return fmt.Errorf("preview admission: %w", preview.ErrQueueFull)
	}
	return nil
}

func TestStagedImport_StopsPreviewEnqueuesAtFirstQueueFull(t *testing.T) {
	const (
		itemCount = 32
		fullAt    = 3
	)
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "enqueue-queue-full@test.local", "admin")
	enqueuer := &queueFullEnqueuer{fullAt: fullAt}
	h := NewHandler(pool, enqueuer)

	imported, skipped, wiped, warnings, err := h.importItemsWithMode(
		context.Background(), uid, importRoundTripFixture("enqueue-queue-full", itemCount), modeSkip, nil,
	)
	require.NoError(t, err)
	assert.Equal(t, itemCount, imported)
	assert.Zero(t, skipped)
	assert.Zero(t, wiped)
	assert.Equal(t, fullAt, enqueuer.calls, "remaining pending rows must rely on set-wise recovery")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], fmt.Sprintf("%d previews", itemCount-fullAt+1))
}

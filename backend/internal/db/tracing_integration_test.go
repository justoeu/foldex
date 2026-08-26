//go:build integration

package db

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"foldex/internal/pkg/spantest"
	"foldex/internal/testdb"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

// No span this process emits may carry user.name.
//
// The unit test above pins what connAttrs RETURNS; this one pins what the pool
// actually EMITS, and only the second one dies when the option that suppresses
// otelpgx's own connection details is removed. That distinction is the whole
// point: an earlier round of this work shipped a test proving a middleware was
// mounted where it had been mounted, which stayed green while the feature was
// broken everywhere else.
//
// The query runs under a SERVER parent because that is the only shape it takes
// in production — internal/tracing's root sampler never samples a root CLIENT
// span, so an unparented pool query (healthz, workers) produces no trace at
// all.
func TestQuerySpansNeverCarryThePostgresRole(t *testing.T) {
	ctx := context.Background()
	// Recorder must precede db.New: otelpgx captures the tracer provider once,
	// at NewTracer time, so a pool built before this line would hold the no-op
	// and record nothing — a test that passes for the wrong reason.
	rec := spantest.Recorder(t)

	dsn := testdb.Shared(t).Config().ConnString()
	pool, err := New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("test")
	spanCtx, parent := tracer.Start(ctx, "parent", oteltrace.WithSpanKind(oteltrace.SpanKindServer))
	var one int
	require.NoError(t, pool.QueryRow(spanCtx, "SELECT 1").Scan(&one))
	parent.End()

	var client sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		for _, kv := range s.Attributes() {
			assert.NotEqual(t, "user.name", string(kv.Key),
				"span %q carries the Postgres role under semconv's user.name, "+
					"which internal/tracing already uses for the ACCOUNT via user.id", s.Name())
		}
		if s.SpanKind() == oteltrace.SpanKindClient {
			client = s
		}
	}

	// Asserting the CLIENT span still describes its server is what keeps the
	// fix honest: disabling otelpgx's connection details WITHOUT re-adding
	// them would also pass the loop above, by emitting nothing at all.
	require.NotNil(t, client, "no CLIENT span was recorded for the query")
	for _, key := range []string{"server.address", "server.port", "db.namespace", "db.system.name"} {
		_, ok := spantest.Attr(client, key)
		assert.True(t, ok, "CLIENT span lost %s", key)
	}
}

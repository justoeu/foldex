//go:build integration

package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"foldex/internal/config"
	"foldex/internal/pkg/spantest"
	"foldex/internal/server"
	"foldex/internal/testdb"
	"foldex/internal/tracing"
	"github.com/stretchr/testify/require"
)

// Covers the bootstrapPrincipal seam — the AUTH_ENABLED=0 arm — through the
// real server.New rather than by calling the middleware directly, because what
// can break is the CALL SITE, not the function: internal/tracing already
// proves the function, and a unit test composing the chain by hand stays green
// when the production call disappears.
//
// The Authenticate and Optional seams are covered the same way in
// internal/auth; internal/security refuses a fourth seam that forgets.
func TestUserIDIsStampedOnRequestSpansThroughTheRealRouter(t *testing.T) {
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "span-owner@test.local", "owner")

	rec := spantest.Recorder(t)

	router := server.New(server.Deps{
		Pool:   pool,
		Worker: nopWorker{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Trace:  tracing.Middleware,
		Config: config.Config{
			Port: "0", CORSOrigins: []string{"*"},
			PreviewConcurrency: 1, PreviewTimeoutSec: 1,
		},
	})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/links", nil))

	require.Equal(t, http.StatusOK, rr.Code, "the request itself must succeed")
	spans := spantest.ServerSpans(rec)
	require.Len(t, spans, 1, "one SERVER span per request")

	got, ok := spantest.Attr(spans[0], "user.id")
	require.True(t, ok, "AnnotatePrincipal is not reaching the request span — check where it is mounted in router.New")
	require.Equal(t, strconv.FormatInt(int64(uid), 10), got,
		"user.id must be the principal the request actually ran as")

	via, ok := spantest.Attr(spans[0], "foldex.auth.via")
	require.True(t, ok)
	require.Equal(t, "session", via)
}

//go:build integration

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/config"
	"foldex/internal/server"
	"foldex/internal/testdb"
)

// Both middlewares this file covers are MOUNTED, and that is the whole point.
//
// INV-170's own history is the argument: an annotation mounted on a route group
// silently lost half the surface it was meant to cover, and every unit test
// kept passing because they exercised the middleware in isolation. The unit
// tests in contentaudit_test.go and blocklist_test.go do exactly that. If
// `r.Use(blocklistGate(...))` or `pr.Use(contentAudit(...))` were deleted from
// router.go, those suites would stay green and the feature would be gone.
//
// So these go through server.New with a real Deps, and assert on effects
// observable from OUTSIDE: a row in audit_log, and a refused request.
func auditWiringServer(t *testing.T) (*httptest.Server, *auth.Repository) {
	t.Helper()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	_ = testdb.SeedUser(t, pool, "owner@test.local", "admin")

	repo := auth.NewRepository(pool)
	router := server.New(server.Deps{
		Pool:   pool,
		Worker: nopWorker{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// The wiring under test: nil unmounts BOTH, which is precisely the
		// state the rest of this package's tests run in.
		AuthRepo: repo,
		Config: config.Config{
			Port: "0", CORSOrigins: []string{"*"},
			PreviewConcurrency: 1, PreviewTimeoutSec: 1,
		},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, repo
}

func post(t *testing.T, srv *httptest.Server, path, body string) *http.Response {
	t.Helper()
	res, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

// A content mutation through the REAL router must land in the trail, with the
// action the route map names and the label the handler annotated.
func TestWiring_ContentAuditRecordsAMutationThroughTheRealRouter(t *testing.T) {
	srv, repo := auditWiringServer(t)

	res := post(t, srv, "/api/tags", `{"name":"reading","color":"#6366F1"}`)
	require.Equal(t, http.StatusCreated, res.StatusCode)

	entries, err := repo.ListAudit(context.Background(),
		auth.AuditFilter{Action: auth.AuditTagCreated})
	require.NoError(t, err)
	require.Len(t, entries, 1, "the middleware is not mounted on the content routes")
	assert.Equal(t, auth.CategoryContent, entries[0].Category)
	// The administrative projection withholds the label even here.
	assert.Nil(t, entries[0].Subject)

	// ...and the OWNER's feed has it, which is the read split end to end.
	require.NotNil(t, entries[0].ActorRef, "the row must still name its actor opaquely")
	own, err := repo.ListOwnActivity(context.Background(), *entries[0].ActorRef, 0, 50)
	require.NoError(t, err)
	require.Len(t, own, 1)
	require.NotNil(t, own[0].Subject)
	assert.Equal(t, "reading", *own[0].Subject)
}

// A rejected write changed nothing and must leave no entry — asserted through
// the router, because the status the middleware reads is the one the real
// handler wrote.
func TestWiring_ContentAuditIgnoresARejectedMutation(t *testing.T) {
	srv, repo := auditWiringServer(t)

	res := post(t, srv, "/api/tags", `{"name":""}`)
	require.GreaterOrEqual(t, res.StatusCode, 400)

	entries, err := repo.ListAudit(context.Background(),
		auth.AuditFilter{Category: auth.CategoryContent})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// The gate must be mounted BEFORE routing: a blocked caller reaches no handler.
func TestWiring_BlocklistGateRefusesBeforeRouting(t *testing.T) {
	srv, repo := auditWiringServer(t)

	// The loopback the test client dials from would be refused by the block
	// RAILS, and rightly — so the row goes in through the repository, which is
	// the only way to reach the state the gate is supposed to enforce.
	_, err := repo.BlockIP(context.Background(), "127.0.0.1", "wiring test", nil, "")
	require.NoError(t, err)

	res, err := http.Get(srv.URL + "/api/tags")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()

	require.Equal(t, http.StatusForbidden, res.StatusCode,
		"the blocklist gate is not mounted on the router")
	var envelope struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&envelope))
	assert.Equal(t, "ip_blocked", envelope.Error.Code)
}

// An instance that declared itself unhealthy because someone blocked a probe's
// address would be restarted in a loop.
func TestWiring_BlocklistGateNeverRefusesTheHealthProbe(t *testing.T) {
	srv, repo := auditWiringServer(t)
	_, err := repo.BlockIP(context.Background(), "127.0.0.1", "wiring test", nil, "")
	require.NoError(t, err)

	res, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	assert.Equal(t, http.StatusOK, res.StatusCode)
}

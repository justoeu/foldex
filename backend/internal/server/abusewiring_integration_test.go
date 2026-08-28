//go:build integration

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/config"
	"foldex/internal/links"
	"foldex/internal/server"
	"foldex/internal/testdb"
)

// Both controls this file covers are MOUNTED, and that is the whole point.
//
// The unit suites in quota_test.go and clickcoalesce_test.go exercise the
// middleware in isolation, and would all stay green if `pr.Use(newAPIQuota…)`
// or the public Group in router.go were deleted — the feature would be gone and
// nothing would say so. INV-170's own history is that exact failure: an
// annotation mounted on the wrong group silently lost half its surface.
//
// So these go through server.New with real Deps and a real database, and assert
// on effects observable from OUTSIDE: a 429 on the wire, and the number of rows
// in click_log.

func abuseWiringServer(t *testing.T) (*httptest.Server, *links.Repository) {
	t.Helper()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	_ = testdb.SeedUser(t, pool, "owner@test.local", "owner")

	router := server.New(server.Deps{
		Pool:   pool,
		Worker: nopWorker{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// AbusePolicy deliberately nil: an unwired policy must still enforce
		// the compiled defaults, and that is what an instance whose owner never
		// opened the screen actually runs.
		Config: config.Config{
			Port: "0", CORSOrigins: []string{"*"},
			PublicNumericIDs:   true,
			PreviewConcurrency: 1, PreviewTimeoutSec: 1,
		},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, links.NewRepository(pool)
}

func countClicks(t *testing.T, kind string, id int64) int {
	t.Helper()
	var n int
	require.NoError(t, testdb.Shared(t).QueryRow(context.Background(),
		`SELECT count(*) FROM click_log WHERE entity_kind = $1 AND entity_id = $2`,
		kind, id).Scan(&n))
	return n
}

// The quota is mounted on the principal group, so an ordinary write loop from
// one account is refused with 429 by the real router. AUTH_ENABLED is off here,
// which means every request is attributed to the bootstrap OWNER — so this also
// proves, end to end, that the owner is not exempt.
func TestWiring_APIQuotaRefusesAWriteLoopThroughTheRealRouter(t *testing.T) {
	srv, _ := abuseWiringServer(t)

	// The compiled default is 120 writes per minute. Walk past it.
	var last *http.Response
	refusedAt := 0
	for i := 0; i < 200; i++ {
		res, err := http.Post(srv.URL+"/api/tags", "application/json",
			strings.NewReader(`{"name":"t`+strconv.Itoa(i)+`","color":"#6366F1"}`))
		require.NoError(t, err)
		if res.StatusCode == http.StatusTooManyRequests {
			refusedAt = i
			last = res
			break
		}
		require.NoError(t, res.Body.Close())
	}
	require.NotNil(t, last, "200 writes in a row were never refused — the quota is not mounted")
	defer func() { _ = last.Body.Close() }()

	assert.Greater(t, refusedAt, 100, "the refusal came far too early to be the 120/min default")

	var envelope struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.NewDecoder(last.Body).Decode(&envelope))
	assert.Equal(t, "rate_limited", envelope.Error.Code)
	assert.NotEmpty(t, last.Header.Get("Retry-After"))
}

// Reading is not metered, so a read loop far longer than the write budget must
// never produce a 429. This is the false-positive side of the same wiring: a
// quota that counted reads would throttle someone browsing their own library.
func TestWiring_APIQuotaNeverRefusesAReadLoop(t *testing.T) {
	srv, _ := abuseWiringServer(t)

	for i := 0; i < 300; i++ {
		res, err := http.Get(srv.URL + "/api/tags")
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		require.NotEqual(t, http.StatusTooManyRequests, res.StatusCode, "read %d was rate limited", i)
	}
}

// The coalescer is mounted on the public group, and the write it suppresses is
// the one the REPOSITORY makes — the two halves only meet through clickctx, so
// this is the only test that proves the pair works.
func TestWiring_RepeatClicksFromOneVisitorWriteOneRowAndStillRedirect(t *testing.T) {
	srv, repo := abuseWiringServer(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, testdb.Shared(t), "vis@test.local", "editor")
	link, err := repo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/go", Title: "Go Target"})
	require.NoError(t, err)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for i := 0; i < 8; i++ {
		res, err := client.Get(srv.URL + "/go/" + link.Slug)
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		require.Equal(t, http.StatusFound, res.StatusCode, "visit %d must still redirect", i+1)
		require.Equal(t, "https://example.com/go", res.Header.Get("Location"),
			"coalescing must suppress the WRITE, never the destination")
	}

	assert.Equal(t, 1, countClicks(t, "link", link.ID),
		"eight hits from one visitor inside the default 10s window must be one row")
}

// The note surface is the same amplifier, and it renders every time.
func TestWiring_RepeatNoteViewsFromOneVisitorWriteOneRowAndStillRender(t *testing.T) {
	srv, _ := abuseWiringServer(t)
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "note@test.local", "editor")

	var id int64
	var slug string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO note (user_id, title, body_html, slug) VALUES ($1,'N','<p>b</p>','wiring-note')
		 RETURNING id, slug`, int64(uid)).Scan(&id, &slug))

	for i := 0; i < 5; i++ {
		res, err := http.Get(srv.URL + "/n/" + slug)
		require.NoError(t, err)
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		require.Equal(t, http.StatusOK, res.StatusCode, "view %d must still render", i+1)
		require.Contains(t, string(body), "<p>b</p>", "the body must be rendered every time")
	}

	assert.Equal(t, 1, countClicks(t, "note", id))
}

// A repository called outside the public HTTP path has no gate in its context,
// and must record every click — the import path, the workers and every existing
// test depend on that default.
func TestWiring_AResolveWithNoGateStillRecordsEveryClick(t *testing.T) {
	_, repo := abuseWiringServer(t)
	ctx := context.Background()
	uid := testdb.SeedUser(t, testdb.Shared(t), "direct@test.local", "editor")
	link, err := repo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/direct", Title: "Direct"})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err := repo.ClickAndResolve(ctx, link.ID)
		require.NoError(t, err)
	}
	assert.Equal(t, 3, countClicks(t, "link", link.ID),
		"an absent gate must mean record, not suppress")
}

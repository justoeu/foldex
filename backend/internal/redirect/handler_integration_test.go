//go:build integration

package redirect_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/redirect"
	"foldex/internal/testdb"

	"foldex/internal/pkg/authctx/authctxtest"
	"os"
)

// TestMain owns the lifetime of this package's shared Postgres container.
//
// It cannot be a t.Cleanup: os.Exit skips deferred work, and a cleanup hung off
// whichever test ran first would tear the database down while the rest of the
// package still needed it. The Makefile disables testcontainers' reaper, so
// nothing else would collect it.
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

// SEC-SEN-002: a title-derived slug is not a capability on /go/ either. An
// anonymous caller who guesses "invoice-portal" must not receive the stored
// destination (bookmark URLs carry reset tokens and shared-doc secrets in
// query strings) nor forge a click_log row owned by the victim.
func TestRedirect_AnonymousSlugResolveIsDeniedWithoutOptIn(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://secret.example/reset?token=guessme", Title: "Invoice Portal",
	})
	require.NoError(t, err)
	require.Equal(t, "invoice-portal", created.Slug, "slug auto-derived from title")

	r := chi.NewRouter()
	redirect.NewHandler(lrepo, false).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(srv.URL + "/go/" + created.Slug)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "an unaided slug guess must not resolve")
	assert.Empty(t, resp.Header.Get("Location"), "and must not disclose the destination")

	var clicks int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM click_log WHERE entity_kind = 'link' AND entity_id = $1`,
		created.ID).Scan(&clicks))
	assert.Zero(t, clicks, "a denied guess must not forge a click row either")
}

// The public share surface stays one opt-in away: is_public keeps anonymous
// /go/{slug} working for links the owner meant to share.
func TestRedirect_OptedInLinkRedirectsForAnonymous(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://news.ycombinator.com", Title: "Hacker News",
	})
	require.NoError(t, err)
	_, err = lrepo.Update(ctx, uid, created.ID, links.UpdateInput{IsPublic: boolPtr(true)})
	require.NoError(t, err)

	r := chi.NewRouter()
	redirect.NewHandler(lrepo, false).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(srv.URL + "/go/" + created.Slug)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "https://news.ycombinator.com", resp.Header.Get("Location"))

	got, _ := lrepo.Get(ctx, uid, created.ID)
	assert.EqualValues(t, 1, got.ClickCount, "an opted-in public click still counts")
}

// Authenticated UX is identical: the owner clicking their own card navigates
// /go/{slug} with the session cookie attached, and nothing changes for them.
func TestRedirect_OwnerSessionRedirectsPrivateLink(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)
	created, err := lrepo.Create(ctx, uid, links.CreateInput{
		URL: "https://example.com/private", Title: "Private Link",
	})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	redirect.NewHandler(lrepo, false).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(srv.URL + "/go/" + created.Slug)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "https://example.com/private", resp.Header.Get("Location"))
}

// A signed-in stranger is in the same position as an anonymous reader:
// owner-or-opted-in, nothing broader.
func TestRedirect_AnotherUsersSessionIsDenied(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	alice := testdb.SeedUser(t, pool, "alice-go@test.local", "admin")
	bravo := testdb.SeedUser(t, pool, "bravo-go@test.local", "admin")
	lrepo := links.NewRepository(pool)
	created, err := lrepo.Create(ctx, alice, links.CreateInput{
		URL: "https://alice.example/secret", Title: "Alice Secret",
	})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(bravo))
	redirect.NewHandler(lrepo, false).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/go/" + created.Slug)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "another tenant's session must not resolve the link")
}

func TestRedirect_HappyPath(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)

	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com", Title: "ex"})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	redirect.NewHandler(lrepo, true).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow
		},
	}
	resp, err := client.Get(srv.URL + "/go/" + intToStr(created.ID))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "https://example.com", resp.Header.Get("Location"))

	got, _ := lrepo.Get(ctx, uid, created.ID)
	assert.EqualValues(t, 1, got.ClickCount)
}

func TestRedirect_NotFound(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	redirect.NewHandler(links.NewRepository(pool), true).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/go/12345")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// /go/abc used to be a 400 (bad ID). With slug-fallback, "abc" is a valid
// candidate slug — we just don't have any link with that slug, so it 404s.
func TestRedirect_NonNumericTargetUnknownSlug404(t *testing.T) {
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	redirect.NewHandler(links.NewRepository(pool), true).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/go/abc")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// /go/{slug} resolves the same link that the create call returned, with the
// click counter incremented post-redirect.
func TestRedirect_BySlugHappyPath(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)

	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://news.ycombinator.com", Title: "Hacker News"})
	require.NoError(t, err)
	require.Equal(t, "hacker-news", created.Slug, "slug auto-derived from title")

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	redirect.NewHandler(lrepo, true).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(srv.URL + "/go/" + created.Slug)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "https://news.ycombinator.com", resp.Header.Get("Location"))

	got, _ := lrepo.Get(ctx, uid, created.ID)
	assert.EqualValues(t, 1, got.ClickCount)
}

// Whatever already worked under /go/{id} has to keep working post-migration.
// Belt-and-suspenders: this is the contract every shared `/go/42` URL relies
// on.
func TestRedirect_ByIDStillWorksAfterSlugFeature(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)

	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com", Title: "ex"})
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(uid))
	redirect.NewHandler(lrepo, true).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(srv.URL + "/go/" + intToStr(created.ID))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "https://example.com", resp.Header.Get("Location"))
}

func TestRedirect_PublicNumericIDsFeatureFlag(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)
	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	repo := links.NewRepository(pool)
	created, err := repo.Create(ctx, uid, links.CreateInput{URL: "https://example.com/flag", Title: "Flag Target"})
	require.NoError(t, err)

	tests := []struct {
		name            string
		target          string
		allowNumericIDs bool
		wantStatus      int
	}{
		{name: "numeric on", target: intToStr(created.ID), allowNumericIDs: true, wantStatus: http.StatusFound},
		{name: "numeric off", target: intToStr(created.ID), allowNumericIDs: false, wantStatus: http.StatusNotFound},
		{name: "slug while numeric off", target: created.Slug, allowNumericIDs: false, wantStatus: http.StatusFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			r.Use(authctxtest.Middleware(uid))
			redirect.NewHandler(repo, tt.allowNumericIDs).Mount(r)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/go/"+tt.target, nil)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := []byte{}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

func boolPtr(b bool) *bool { return &b }

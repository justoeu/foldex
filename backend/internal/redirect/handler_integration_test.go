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

func TestRedirect_HappyPath(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
	lrepo := links.NewRepository(pool)

	created, err := lrepo.Create(ctx, uid, links.CreateInput{URL: "https://example.com", Title: "ex"})
	require.NoError(t, err)

	r := chi.NewRouter()
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

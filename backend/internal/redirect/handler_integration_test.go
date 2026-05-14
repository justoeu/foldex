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
)

func TestRedirect_HappyPath(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	lrepo := links.NewRepository(pool)

	created, err := lrepo.Create(ctx, links.CreateInput{URL: "https://example.com", Title: "ex"})
	require.NoError(t, err)

	r := chi.NewRouter()
	redirect.NewHandler(lrepo).Mount(r)
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

	got, _ := lrepo.Get(ctx, created.ID)
	assert.EqualValues(t, 1, got.ClickCount)
}

func TestRedirect_NotFound(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	redirect.NewHandler(links.NewRepository(pool)).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/go/12345")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestRedirect_InvalidID(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	redirect.NewHandler(links.NewRepository(pool)).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/go/abc")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
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

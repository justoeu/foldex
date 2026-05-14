//go:build integration

package stats_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
	"foldex/internal/stats"
	"foldex/internal/testdb"
)

func TestHandler_RoutesAndShapes(t *testing.T) {
	pool := testdb.New(t)
	srepo := stats.NewRepository(pool)
	r := chi.NewRouter()
	stats.NewHandler(srepo).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Seed: one link + 2 clicks so non-zero numbers come back.
	lrepo := links.NewRepository(pool)
	l, _ := lrepo.Create(context.Background(), links.CreateInput{URL: "https://hn.example", Title: "HN"})
	_, _ = lrepo.ClickAndResolve(context.Background(), l.ID)
	_, _ = lrepo.ClickAndResolve(context.Background(), l.ID)

	t.Run("summary", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/summary")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var s stats.Summary
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&s))
		assert.EqualValues(t, 1, s.TotalLinks)
		assert.EqualValues(t, 2, s.TotalClicks)
	})

	t.Run("daily default 60d", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/daily")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var out []stats.DailyPoint
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		assert.Len(t, out, 60)
	})

	t.Run("daily clamped", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/daily?days=7")
		require.NoError(t, err)
		defer resp.Body.Close()
		var out []stats.DailyPoint
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		assert.Len(t, out, 7)
	})

	t.Run("top", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/top?limit=5")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var out []stats.TopLink
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		require.Len(t, out, 1)
		assert.Equal(t, "HN", out[0].Title)
		assert.EqualValues(t, 2, out[0].Clicks)
	})

	t.Run("tags", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/tags")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var out []stats.TagBucket
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		// No tags yet → empty array.
		assert.Empty(t, out)
	})
}

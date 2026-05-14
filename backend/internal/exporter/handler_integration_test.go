//go:build integration

package exporter_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/exporter"
	"foldex/internal/links"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func seed(t *testing.T) (*chi.Mux, func()) {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	trepo := tags.NewRepository(pool)
	lrepo := links.NewRepository(pool)

	tag, _ := trepo.Create(ctx, tags.CreateInput{Name: "jira", Color: "#1f6feb"})
	_, _ = lrepo.Create(ctx, links.CreateInput{
		URL: "https://jira.example/INV-1", Title: "INV-1", TagIDs: []int64{tag.ID},
	})

	r := chi.NewMux()
	exporter.NewHandler(pool).Mount(r)
	return r, pool.Close
}

func TestExportNetscape_ContainsLinkAndFolder(t *testing.T) {
	r, _ := seed(t)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?format=netscape")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	assert.Contains(t, got, "<!DOCTYPE NETSCAPE-Bookmark-file-1>")
	assert.Contains(t, got, "<H3>jira</H3>")
	assert.Contains(t, got, "https://jira.example/INV-1")
}

func TestExportJSON_HasVersion(t *testing.T) {
	r, _ := seed(t)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?format=json")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(got), "{"))
	// JSON export bumped to v2 when folders were added — see CLAUDE.md
	// "JSON v2 ganhou folders[]".
	assert.Contains(t, got, `"version":2`)
	assert.Contains(t, got, "jira.example")
}

func TestExport_BadFormat(t *testing.T) {
	r, _ := seed(t)
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/?format=xml")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

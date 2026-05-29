//go:build integration

package importer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/importer"
	"foldex/internal/links"
	"foldex/internal/testdb"
)

type fakeEnqueuer struct{ ids []int64 }

func (f *fakeEnqueuer) Enqueue(id int64) error { f.ids = append(f.ids, id); return nil }

func multipartBody(t *testing.T, format, content string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	require.NoError(t, mw.WriteField("format", format))
	fw, err := mw.CreateFormFile("file", "bookmarks.html")
	require.NoError(t, err)
	_, err = fw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return body, mw.FormDataContentType()
}

func TestImportNetscape_HappyPath(t *testing.T) {
	pool := testdb.New(t)
	enq := &fakeEnqueuer{}
	r := chi.NewRouter()
	importer.NewHandler(pool, enq).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	html := `<DL>
		<DT><H3>Jira</H3>
		<DL>
			<DT><A HREF="https://jira.example/1">INV-1</A>
			<DT><A HREF="https://jira.example/2">INV-2</A>
		</DL>
	</DL>`
	body, ct := multipartBody(t, "netscape", html)
	resp, err := http.Post(srv.URL+"/", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct{ Imported, Skipped int }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, 2, out.Imported)
	assert.Equal(t, 0, out.Skipped)
	assert.Len(t, enq.ids, 2)

	// re-import the same file → all skipped (idempotent by URL)
	body, ct = multipartBody(t, "netscape", html)
	resp, err = http.Post(srv.URL+"/", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, 0, out.Imported)
	assert.Equal(t, 2, out.Skipped)

	// folder was created — the Netscape parser maps the deepest <H3>
	// (here "Jira") to a foldex folder (CLAUDE.md: "<H3> mais profundo vira
	// folder, H3s ancestrais viram tags").
	rows, _ := pool.Query(context.Background(), `SELECT name FROM folder`)
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	assert.Contains(t, names, "Jira")
}

func TestImportJSON_RoundTrip(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	payload := `{
        "version": 1,
        "tags": [{"name":"docs","color":"#a78bfa","icon":"📚"}],
        "links": [{
            "url": "https://docs.example",
            "title": "Docs",
            "tags": ["docs"],
            "click_count": 5,
            "created_at": "2025-01-15T08:00:00Z"
        }]
    }`
	body, ct := multipartBody(t, "json", payload)
	resp, err := http.Post(srv.URL+"/", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	lrepo := links.NewRepository(pool)
	list, err := lrepo.List(context.Background(), links.ListQuery{})
	require.NoError(t, err)
	require.Len(t, list, 1)

	l := list[0]
	require.Len(t, l.Tags, 1)
	assert.Equal(t, "docs", l.Tags[0].Name)
	assert.Equal(t, "#a78bfa", l.Tags[0].Color)

	// click_count and created_at must be restored from the export
	assert.Equal(t, int64(5), l.ClickCount)
	want, _ := time.Parse(time.RFC3339, "2025-01-15T08:00:00Z")
	assert.True(t, l.CreatedAt.Equal(want), "created_at mismatch: got %v want %v", l.CreatedAt, want)
}

func TestImportJSON_EmptyTitleFallsBackToURL(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	payload := `{"version":1,"tags":[],"links":[{"url":"https://notitle.example","title":""}]}`
	body, ct := multipartBody(t, "json", payload)
	resp, err := http.Post(srv.URL+"/", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	lrepo := links.NewRepository(pool)
	list, err := lrepo.List(context.Background(), links.ListQuery{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "https://notitle.example", list[0].Title)
}

func TestImportJSON_InvalidVersion(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	payload := `{"version":99,"tags":[],"links":[]}`
	body, ct := multipartBody(t, "json", payload)
	resp, err := http.Post(srv.URL+"/", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestImportJSON_InvalidURL(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	payload := `{"version":1,"tags":[],"links":[{"url":"not-a-url","title":"t"}]}`
	body, ct := multipartBody(t, "json", payload)
	resp, err := http.Post(srv.URL+"/", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestImport_BadFormat(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, ct := multipartBody(t, "xml", "<x/>")
	resp, err := http.Post(srv.URL+"/", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestImport_BodyTooLarge(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Build a multipart body whose file part exceeds the handler's cap
	// (currently 100 MB — keep this test in lockstep with maxUploadBytes
	// in handler.go).
	const overCap = 101
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	require.NoError(t, mw.WriteField("format", "netscape"))
	fw, err := mw.CreateFormFile("file", "big.html")
	require.NoError(t, err)
	chunk := bytes.Repeat([]byte("A"), 1<<20) // 1 MB chunk
	for i := 0; i < overCap; i++ {
		_, err = fw.Write(chunk)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())

	resp, err := http.Post(srv.URL+"/", mw.FormDataContentType(), body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

func TestImport_MissingFile(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField("format", "netscape")
	_ = mw.Close()

	resp, err := http.Post(srv.URL+"/", mw.FormDataContentType(), body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

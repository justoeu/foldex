//go:build integration

package importer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/importer"
	"foldex/internal/links"
	"foldex/internal/tags"
	"foldex/internal/testdb"
)

func multipartFields(t *testing.T, fields map[string]string, content string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	fw, err := mw.CreateFormFile("file", "bookmarks.html")
	require.NoError(t, err)
	_, err = fw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return body, mw.FormDataContentType()
}

func TestValidate_ReportsConflictsAndFolders(t *testing.T) {
	pool := testdb.New(t)
	lrepo := links.NewRepository(pool)
	trepo := tags.NewRepository(pool)
	ctx := context.Background()

	// Outer H3 becomes a tag; seed it so Conflicts.Tags increments.
	_, err := trepo.Create(ctx, tags.CreateInput{Name: "Outer", Color: "#abc"})
	require.NoError(t, err)
	_, err = lrepo.Create(ctx, links.CreateInput{URL: "https://exists.example", Title: "Exists"})
	require.NoError(t, err)

	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Nested H3: Outer → tag, Work → folder.
	html := `<DL>
		<DT><H3>Outer</H3>
		<DL>
			<DT><H3>Work</H3>
			<DL>
				<DT><A HREF="https://exists.example">Exists</A>
				<DT><A HREF="https://fresh.example">Fresh</A>
			</DL>
		</DL>
	</DL>`
	body, ct := multipartFields(t, map[string]string{"format": "netscape"}, html)
	resp, err := http.Post(srv.URL+"/validate", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rep importer.ValidationReport
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rep))
	assert.Equal(t, "netscape", rep.Format)
	assert.Equal(t, 2, rep.Counts.Links)
	assert.GreaterOrEqual(t, rep.Counts.Folders, 1)
	assert.Equal(t, 1, rep.Conflicts.Links)
	assert.Equal(t, 1, rep.Conflicts.Tags)
	require.NotEmpty(t, rep.Folders)

	var conflicted, fresh bool
	for _, l := range rep.Links {
		if l.URL == "https://exists.example" {
			assert.True(t, l.Conflict)
			conflicted = true
		}
		if l.URL == "https://fresh.example" {
			assert.False(t, l.Conflict)
			fresh = true
		}
	}
	assert.True(t, conflicted && fresh)
}

func TestValidate_EmptyFile(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, ct := multipartFields(t, map[string]string{"format": "netscape"}, `<DL></DL>`)
	resp, err := http.Post(srv.URL+"/validate", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rep importer.ValidationReport
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rep))
	assert.Equal(t, 0, rep.Counts.Links)
	assert.Equal(t, 0, rep.Conflicts.Links)
}

func TestApply_SkipMode_Default(t *testing.T) {
	pool := testdb.New(t)
	lrepo := links.NewRepository(pool)
	ctx := context.Background()
	_, err := lrepo.Create(ctx, links.CreateInput{URL: "https://dup.example", Title: "Old"})
	require.NoError(t, err)

	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	html := `<DL>
		<DT><A HREF="https://dup.example">New Title</A>
		<DT><A HREF="https://brand-new.example">Brand</A>
	</DL>`
	body, ct := multipartFields(t, map[string]string{"format": "netscape", "mode": "skip"}, html)
	resp, err := http.Post(srv.URL+"/apply", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Imported, Skipped, Wiped int
		Mode                     string
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, "skip", out.Mode)
	assert.Equal(t, 1, out.Imported)
	assert.Equal(t, 1, out.Skipped)
	assert.Equal(t, 0, out.Wiped)
}

func TestApply_WipeMode(t *testing.T) {
	pool := testdb.New(t)
	lrepo := links.NewRepository(pool)
	ctx := context.Background()
	old, err := lrepo.Create(ctx, links.CreateInput{URL: "https://wipe-me.example", Title: "Old"})
	require.NoError(t, err)

	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	html := `<DL><DT><A HREF="https://wipe-me.example">Replaced</A></DL>`
	body, ct := multipartFields(t, map[string]string{"format": "netscape", "mode": "wipe"}, html)
	resp, err := http.Post(srv.URL+"/apply", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Imported, Skipped, Wiped int
		Mode                     string
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, "wipe", out.Mode)
	assert.Equal(t, 1, out.Imported)
	assert.Equal(t, 1, out.Wiped)

	list, err := lrepo.List(ctx, links.ListQuery{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "Replaced", list[0].Title)
	assert.NotEqual(t, old.ID, list[0].ID)
}

func TestApply_DuplicateMode_WarnsOnURLCollision(t *testing.T) {
	pool := testdb.New(t)
	lrepo := links.NewRepository(pool)
	ctx := context.Background()
	_, err := lrepo.Create(ctx, links.CreateInput{URL: "https://keep.example", Title: "Keep"})
	require.NoError(t, err)

	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	html := `<DL>
		<DT><A HREF="https://keep.example">Would Dup</A>
		<DT><A HREF="https://other.example">Other</A>
	</DL>`
	body, ct := multipartFields(t, map[string]string{"format": "netscape", "mode": "duplicate"}, html)
	resp, err := http.Post(srv.URL+"/apply", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Imported, Skipped int
		Warnings          []string
		Mode              string
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, "duplicate", out.Mode)
	assert.Equal(t, 1, out.Imported)
	assert.Equal(t, 1, out.Skipped)
	require.NotEmpty(t, out.Warnings)
	assert.Contains(t, out.Warnings[0], "https://keep.example")
}

func TestApply_ExcludeFolders(t *testing.T) {
	pool := testdb.New(t)
	lrepo := links.NewRepository(pool)
	ctx := context.Background()

	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Deepest H3 becomes the folder name.
	html := `<DL>
		<DT><H3>Keep</H3>
		<DL><DT><A HREF="https://keep-folder.example">K</A></DL>
		<DT><H3>SkipMe</H3>
		<DL><DT><A HREF="https://skip-folder.example">S</A></DL>
	</DL>`
	body, ct := multipartFields(t, map[string]string{
		"format":          "netscape",
		"mode":            "skip",
		"exclude_folders": "SkipMe",
	}, html)
	resp, err := http.Post(srv.URL+"/apply", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct{ Imported int }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, 1, out.Imported)

	list, err := lrepo.List(ctx, links.ListQuery{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "https://keep-folder.example", list[0].URL)
}

func TestApply_JSONWithSeedColors(t *testing.T) {
	pool := testdb.New(t)
	lrepo := links.NewRepository(pool)
	ctx := context.Background()

	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Compact single-line payload (avoids multipart newline surprises).
	payload := `{"version":1,"tags":[{"name":"seeded","color":"#ff0000"}],"folders":[{"name":"SeedFolder","color":"#00ff00"}],"links":[{"url":"https://seeded-apply.example","title":"Seeded Apply","tags":["seeded"],"folder":"SeedFolder","click_count":3,"created_at":"2025-03-01T10:00:00Z"}]}`
	body, ct := multipartFields(t, map[string]string{"format": "json", "mode": "skip"}, payload)
	resp, err := http.Post(srv.URL+"/apply", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", raw)

	list, err := lrepo.List(ctx, links.ListQuery{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.EqualValues(t, 3, list[0].ClickCount)
	require.Len(t, list[0].Tags, 1)
	assert.Equal(t, "#ff0000", list[0].Tags[0].Color)
	require.NotNil(t, list[0].FolderID)

	var folderColor string
	require.NoError(t, pool.QueryRow(ctx, `SELECT color FROM folder WHERE id = $1`, *list[0].FolderID).Scan(&folderColor))
	assert.Equal(t, "#00ff00", folderColor)
}

func TestApply_EmptyModeDefaultsToSkip(t *testing.T) {
	pool := testdb.New(t)
	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	html := `<DL><DT><A HREF="https://empty-mode.example">E</A></DL>`
	body, ct := multipartFields(t, map[string]string{"format": "netscape"}, html)
	resp, err := http.Post(srv.URL+"/apply", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct{ Mode string }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, "skip", out.Mode)
}

func TestApply_JSONValidateViaApply(t *testing.T) {
	// validate endpoint with JSON format + existing URL conflict
	pool := testdb.New(t)
	lrepo := links.NewRepository(pool)
	ctx := context.Background()
	_, err := lrepo.Create(ctx, links.CreateInput{URL: "https://json-exists.example", Title: "X"})
	require.NoError(t, err)

	r := chi.NewRouter()
	importer.NewHandler(pool, &fakeEnqueuer{}).Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	payload := `{"version":1,"tags":[],"links":[
		{"url":"https://json-exists.example","title":"X"},
		{"url":"https://json-new.example","title":"N","folder":"F"}
	]}`
	body, ct := multipartFields(t, map[string]string{"format": "json"}, payload)
	resp, err := http.Post(srv.URL+"/validate", ct, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rep importer.ValidationReport
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rep))
	assert.Equal(t, "json", rep.Format)
	assert.Equal(t, 2, rep.Counts.Links)
	assert.Equal(t, 1, rep.Conflicts.Links)
	assert.Equal(t, 1, rep.Counts.Folders)
}

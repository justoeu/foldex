package importer

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/testsupport"
)

// multiFieldBody builds a multipart body that can repeat one field name,
// which the map-based multipartBody helper cannot express.
func multiFieldBody(t *testing.T, fields [][2]string, fileContent string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		require.NoError(t, w.WriteField(f[0], f[1]))
	}
	part, err := w.CreateFormFile("file", "b.html")
	require.NoError(t, err)
	_, _ = part.Write([]byte(fileContent))
	require.NoError(t, w.Close())

	req := httptest.NewRequest(http.MethodPost, "/import/apply", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	require.NoError(t, req.ParseMultipartForm(maxMultipartMemory))
	return req
}

func TestExcludedFolders_V2RepeatedFieldsKeepCommas(t *testing.T) {
	req := multiFieldBody(t, [][2]string{
		{"exclude_folders_v2", "1"},
		{"exclude_folders", "Work, Inc"},
		{"exclude_folders", "  Personal "},
		{"exclude_folders", "  "},
	}, "<x/>")
	got := excludedFolders(req)
	require.Len(t, got, 2, "a v2 field is one folder, never CSV-split")
	_, ok := got["Work, Inc"]
	assert.True(t, ok)
	_, ok = got["Personal"]
	assert.True(t, ok)

	// v2 switched on but no repeated values: fall back to the legacy CSV read.
	legacy := multiFieldBody(t, [][2]string{{"exclude_folders_v2", "1"}}, "<x/>")
	assert.Empty(t, excludedFolders(legacy))

	// Without the v2 flag the repeated fields collapse through FormValue (CSV).
	plain := multiFieldBody(t, [][2]string{{"exclude_folders", "A, B"}}, "<x/>")
	assert.Equal(t, map[string]struct{}{"A": {}, "B": {}}, excludedFolders(plain))
}

func TestFilterSeedFolders_NilAndExclusions(t *testing.T) {
	assert.Nil(t, filterSeedFolders(nil, map[string]struct{}{"X": {}}),
		"a Netscape upload has no seed and must stay seedless")

	seed := &jsonSeed{
		tags:    []JSONTag{{Name: "go"}},
		folders: []JSONFolder{{Name: " Work "}, {Name: "Keep"}, {Name: "Drop"}},
	}
	out := filterSeedFolders(seed, map[string]struct{}{"Work": {}, "Drop": {}})
	require.NotNil(t, out)
	require.Len(t, out.folders, 1, "an excluded seed folder must not be recreated empty")
	assert.Equal(t, "Keep", out.folders[0].Name)
	assert.Equal(t, seed.tags, out.tags, "folder filtering leaves the tag seed alone")
}

func TestParseJSONUpload_SuccessCarriesSeed(t *testing.T) {
	rec := httptest.NewRecorder()
	up, err := parseJSONUpload(rec, bytes.NewBufferString(
		`{"version":2,"tags":[{"name":"go","color":"#abc"}],"folders":[{"name":"Docs","color":"#111"}],
		  "links":[{"url":"https://a.test","title":"A","tags":["go"],"folder":"Docs"}]}`), "json")
	require.NoError(t, err)
	assert.Equal(t, "json", up.format)
	require.Len(t, up.items, 1)
	assert.Equal(t, "https://a.test", up.items[0].URL)
	require.NotNil(t, up.seed)
	assert.Equal(t, []JSONTag{{Name: "go", Color: "#abc"}}, up.seed.tags)
	assert.Equal(t, []JSONFolder{{Name: "Docs", Color: "#111"}}, up.seed.folders)
}

// closedPoolHandler mounts a handler whose stager fails at Begin, so the
// post-parse error writes of handle/validate/apply run without a database.
func closedPoolHandler(t *testing.T) http.Handler {
	t.Helper()
	h := NewHandler(NewStager(testsupport.ClosedPool(t)), nil)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := authctx.WithPrincipal(req.Context(), authctx.Principal{UserID: 42})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/import", h.Mount)
	return r
}

const oneLinkNetscape = `<!DOCTYPE NETSCAPE-Bookmark-file-1><DL><p><DT><A HREF="https://a.test">A</A></DL>`

func TestHandler_DBFailureIsSurfacedNotSwallowed(t *testing.T) {
	r := closedPoolHandler(t)
	for _, path := range []string{"/import", "/import/validate", "/import/apply"} {
		t.Run(path, func(t *testing.T) {
			body, ct := multipartBody(t, map[string]string{"format": "netscape", "mode": "skip"},
				"file", "b.html", oneLinkNetscape)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, body)
			req.Header.Set("Content-Type", ct)
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusInternalServerError, rec.Code,
				"a well-formed upload that dies in the stager is a server error, not a 400")
		})
	}
}

func TestImportItemsWithMode_StagerErrorPropagatesAndSkipsWorker(t *testing.T) {
	h := NewHandler(NewStager(testsupport.ClosedPool(t)), queueFullWorker{})
	imp, _, _, _, err := h.importItemsWithMode(context.Background(), authctx.UserID(42),
		[]Item{{URL: "https://a.test", Title: "A", ClickCount: -1}}, modeSkip, nil)
	require.Error(t, err)
	assert.Zero(t, imp)
	assert.Contains(t, err.Error(), "click_count out of range",
		"the budget check must fire before any pool or worker interaction")
}

func TestApplyWithMode_ClosedPoolFailsAtBegin(t *testing.T) {
	s := NewStager(testsupport.ClosedPool(t))
	_, _, _, _, _, err := s.ApplyWithMode(context.Background(), authctx.UserID(1),
		[]Item{{URL: "https://a.test", Title: "A"}}, modeSkip, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "begin import tx")
}

func TestValidate_ClosedPoolFailsOnlyWhenDBIsNeeded(t *testing.T) {
	pool := testsupport.ClosedPool(t)
	// No items => the conflict queries short-circuit and the report is valid.
	rep, err := Validate(context.Background(), pool, authctx.UserID(1), nil)
	require.NoError(t, err)
	assert.Zero(t, rep.Counts.Links)

	_, err = Validate(context.Background(), pool, authctx.UserID(1),
		[]Item{{URL: "https://a.test", Title: "A"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflict links")
}

type queueFullWorker struct{}

func (queueFullWorker) Enqueue(int64) error { return errEnqueueSentinel }

var errEnqueueSentinel = errorString("worker must not be reached on a failed apply")

type errorString string

func (e errorString) Error() string { return string(e) }

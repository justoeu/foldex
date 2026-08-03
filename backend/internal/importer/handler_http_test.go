package importer

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mountImporter(t *testing.T) http.Handler {
	t.Helper()
	// nil pool — routes that need DB aren't hit; parse/mode errors fire first.
	h := NewHandler(nil, nil)
	r := chi.NewRouter()
	r.Route("/import", h.Mount)
	return r
}

func multipartBody(t *testing.T, fields map[string]string, fileField, fileName, fileContent string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(t, w.WriteField(k, v))
	}
	if fileField != "" {
		part, err := w.CreateFormFile(fileField, fileName)
		require.NoError(t, err)
		_, _ = part.Write([]byte(fileContent))
	}
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

func TestHandler_Apply_BadMode(t *testing.T) {
	r := mountImporter(t)
	body, ct := multipartBody(t, map[string]string{"format": "netscape", "mode": "nope"}, "file", "b.html", "<a href=\"https://x.com\">x</a>")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import/apply", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "bad_mode")
}

func TestHandler_Validate_MissingFile(t *testing.T) {
	r := mountImporter(t)
	body, ct := multipartBody(t, map[string]string{"format": "netscape"}, "", "", "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "missing_file")
}

func TestHandler_Validate_UnknownFormat(t *testing.T) {
	r := mountImporter(t)
	body, ct := multipartBody(t, map[string]string{"format": "xml"}, "file", "b.xml", "<x/>")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "unknown_format")
}

func TestHandler_Legacy_UnknownFormat(t *testing.T) {
	r := mountImporter(t)
	body, ct := multipartBody(t, map[string]string{"format": "csv"}, "file", "b.csv", "a,b")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "unknown_format")
}

func TestHandler_Apply_MissingFile(t *testing.T) {
	r := mountImporter(t)
	body, ct := multipartBody(t, map[string]string{"format": "netscape", "mode": "skip"}, "", "", "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import/apply", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "missing_file")
}

func TestHandler_Validate_TooManyJSONLinks(t *testing.T) {
	r := mountImporter(t)
	// Build a minimal JSON with maxImportItems+1 links without materializing
	// full validation (count check runs before Validate).
	var b strings.Builder
	b.WriteString(`{"version":2,"tags":[],"folders":[],"links":[`)
	for i := 0; i < maxImportItems+1; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"url":"https://x.test/`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`","title":"t"}`)
	}
	b.WriteString(`]}`)
	body, ct := multipartBody(t, map[string]string{"format": "json"}, "file", "b.json", b.String())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "too_many_items")
}

func TestHandler_Validate_JSONParseFailed(t *testing.T) {
	r := mountImporter(t)
	body, ct := multipartBody(t, map[string]string{"format": "json"}, "file", "b.json", "{not-json")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "parse_failed")
}

func TestHandler_Validate_JSONValidationFailed(t *testing.T) {
	r := mountImporter(t)
	// version 99 is rejected by JSONFile.Validate
	body, ct := multipartBody(t, map[string]string{"format": "json"}, "file", "b.json",
		`{"version":99,"tags":[],"links":[]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "validation_failed")
}

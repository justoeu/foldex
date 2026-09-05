package importer

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// TestImport_OverlappingUploadsAreRejectedBeforeTempSpill is the RED for
// BP-HYD-003: handle/validate/apply all ParseMultipartForm (32 MiB heap, then
// temp spill) with no in-flight slot, so overlapping uploads all enter parse.
func TestImport_OverlappingUploadsAreRejectedBeforeTempSpill(t *testing.T) {
	h := NewHandler(nil, nil)
	r := chi.NewRouter()
	r.Route("/import", h.Mount)

	hold := make(chan struct{})
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	var firstReads atomic.Int64
	go func() {
		defer func() {
			_ = mw.Close()
			_ = pw.Close()
		}()
		_ = mw.WriteField("format", "xml")
		part, err := mw.CreateFormFile("file", "b.xml")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_, _ = part.Write([]byte("<x/>"))
		<-hold
	}()

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/import/validate", &readCounter{r: pr, n: &firstReads})
		req.Header.Set("Content-Type", mw.FormDataContentType())
		r.ServeHTTP(rec, req)
		firstDone <- rec
	}()

	deadline := time.Now().Add(2 * time.Second)
	for firstReads.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("first import never started reading the multipart body")
		}
		time.Sleep(5 * time.Millisecond)
	}

	secondBody, ct := multipartBody(t, map[string]string{"format": "xml"}, "file", "b.xml", "<x/>")
	var secondReads atomic.Int64
	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/import/validate", &readCounter{r: bytes.NewReader(secondBody.Bytes()), n: &secondReads})
		req.Header.Set("Content-Type", ct)
		r.ServeHTTP(rec, req)
		secondDone <- rec
	}()

	select {
	case rec := <-secondDone:
		require.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.Equal(t, "1", rec.Header().Get("Retry-After"))
		assert.Contains(t, rec.Body.String(), "import_busy")
		assert.Zero(t, secondReads.Load(), "rejected upload must not read the body (no temp spill)")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second import blocked in ParseMultipartForm instead of 429 before the temp spill")
	}

	close(hold)
	first := <-firstDone
	assert.Equal(t, http.StatusBadRequest, first.Code)
	assert.Contains(t, first.Body.String(), "unknown_format")
}

type readCounter struct {
	r io.Reader
	n *atomic.Int64
}

func (c *readCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *readCounter) Close() error {
	if closer, ok := c.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

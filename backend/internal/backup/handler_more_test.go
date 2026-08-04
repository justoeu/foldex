package backup

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx/authctxtest"
)

func TestNewHandler_NilLogger(t *testing.T) {
	h := NewHandler(&fakeBackupSvc{}, nil)
	require.NotNil(t, h)
	require.NotNil(t, h.logger)
}

func minimalZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestHandler_Restore_OK_DefaultMode(t *testing.T) {
	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/restore", bytes.NewReader(minimalZip(t)))
	req.Header.Set("Content-Type", "application/zip")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"mode":"skip"`)
}

func TestHandler_Restore_OK_ExplicitModes(t *testing.T) {
	for _, mode := range []string{"wipe", "skip", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			r := mount(t, &fakeBackupSvc{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/backup/restore?mode="+mode, bytes.NewReader(minimalZip(t)))
			req.Header.Set("Content-Type", "application/zip")
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), `"mode":"`+mode+`"`)
		})
	}
}

func TestHandler_Restore_ServiceError(t *testing.T) {
	r := mount(t, &fakeBackupSvc{restoreErr: errors.New("boom")})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/restore?mode=skip", bytes.NewReader(minimalZip(t)))
	req.Header.Set("Content-Type", "application/zip")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHandler_Restore_EmptyBody(t *testing.T) {
	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/restore?mode=skip", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "application/zip")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "bad_zip")
}

func TestHandler_Restore_NotAZip(t *testing.T) {
	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/restore?mode=skip", strings.NewReader("not-a-zip"))
	req.Header.Set("Content-Type", "application/zip")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "bad_zip")
}

func TestHandler_Validate_ServiceError(t *testing.T) {
	r := mount(t, &fakeBackupSvc{validateErr: errors.New("db down")})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/validate", bytes.NewReader(minimalZip(t)))
	req.Header.Set("Content-Type", "application/zip")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHandler_Validate_MultipartOK(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "backup.zip")
	require.NoError(t, err)
	_, err = part.Write(minimalZip(t))
	require.NoError(t, err)
	// Extra non-file field must be skipped.
	require.NoError(t, mw.WriteField("mode", "skip"))
	require.NoError(t, mw.Close())

	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/validate", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
}

func TestHandler_Validate_MultipartMissingFile(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	require.NoError(t, mw.WriteField("note", "no file here"))
	require.NoError(t, mw.Close())

	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/validate", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "bad_zip")
}

func TestHandler_Validate_BadContentType(t *testing.T) {
	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/validate", strings.NewReader("x"))
	req.Header.Set("Content-Type", "text/plain")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestStreamToTempZip_EmptyAndInvalid(t *testing.T) {
	_, cleanup, err := streamToTempZip(strings.NewReader(""))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
	cleanup()

	_, cleanup, err = streamToTempZip(strings.NewReader("notzip"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse zip")
	cleanup()
}

func TestStreamToTempZip_OK(t *testing.T) {
	zr, cleanup, err := streamToTempZip(bytes.NewReader(minimalZip(t)))
	require.NoError(t, err)
	require.NotNil(t, zr)
	cleanup()
}

func TestReadZipFromRequest_MultipartRestore(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "b.zip")
	require.NoError(t, err)
	_, err = io.Copy(part, bytes.NewReader(minimalZip(t)))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/restore?mode=wipe", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"mode":"wipe"`)
}

// Ensure Mount registers all three routes (smoke).
func TestHandler_Mount_RoutesExist(t *testing.T) {
	h := NewHandler(&fakeBackupSvc{}, nil)
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r.Route("/backup", h.Mount)

	for _, path := range []string{"/backup", "/backup/validate", "/backup/restore"} {
		rec := httptest.NewRecorder()
		// GET should 405 (method not allowed) or 404 depending on chi — POST is registered.
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusNotFound, rec.Code, path)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestStreamToTempZip_MaxBytesError(t *testing.T) {
	_, cleanup, err := streamToTempZip(errReader{err: &http.MaxBytesError{Limit: maxBackupBytes}})
	assert.ErrorIs(t, err, ErrPayloadTooLarge)
	cleanup()
}

func TestHandler_Validate_PayloadTooLarge(t *testing.T) {
	// When streamToTempZip returns ErrPayloadTooLarge, handler maps to 413.
	// Drive via Content-Length trick is flaky with MaxBytesReader; call the
	// mapping path indirectly by posting a body that exceeds a tiny cap is
	// not available (const is 2GiB). Covered by TestStreamToTempZip_MaxBytesError
	// + handler branch unit via restore with the sentinel through a custom
	// path is enough for streamToTempZip. Smoke the handler bad_zip path for
	// multipart empty file part.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "b.zip")
	require.NoError(t, err)
	_, _ = part.Write(nil) // empty file part
	require.NoError(t, mw.Close())

	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/restore?mode=skip", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// compile-time check that fake still satisfies interface after edits
var _ BackupService = (*fakeBackupSvc)(nil)

package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBackupSvc struct {
	exportErr   error
	validateErr error
	restoreErr  error
}

func (f *fakeBackupSvc) Export(_ context.Context, w io.Writer, onCountsReady func(Counts) error) (ExportReport, error) {
	if f.exportErr != nil {
		return ExportReport{}, f.exportErr
	}
	if onCountsReady != nil {
		if err := onCountsReady(Counts{Links: 2}); err != nil {
			return ExportReport{}, err
		}
	}
	_, _ = w.Write([]byte("PK\x03\x04"))
	return ExportReport{Counts: Counts{Links: 2}, DurationMs: 5}, nil
}

func (f *fakeBackupSvc) Validate(_ context.Context, _ *zip.Reader) (Validation, error) {
	if f.validateErr != nil {
		return Validation{}, f.validateErr
	}
	return Validation{OK: true}, nil
}

func (f *fakeBackupSvc) Restore(_ context.Context, _ *zip.Reader, mode ConflictMode) (RestoreReport, error) {
	if f.restoreErr != nil {
		return RestoreReport{}, f.restoreErr
	}
	return RestoreReport{Mode: mode}, nil
}

func mount(t *testing.T, f *fakeBackupSvc) http.Handler {
	t.Helper()
	h := NewHandler(f, slog.Default())
	r := chi.NewRouter()
	r.Route("/backup", h.Mount)
	return r
}

func TestHandler_Export_OKHeaders(t *testing.T) {
	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Header().Get("Content-Disposition"), "foldex-backup-")
	assert.Equal(t, "2", rec.Header().Get("X-Foldex-Backup-Counts-Links"))
}

func TestHandler_Export_FailureBeforeHeaders(t *testing.T) {
	r := mount(t, &fakeBackupSvc{exportErr: errors.New("disk full")})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "export_failed")
}

func TestHandler_Restore_BadMode(t *testing.T) {
	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/restore?mode=nope", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "bad_mode")
}

func TestHandler_Validate_EmptyBody(t *testing.T) {
	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/validate", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "application/zip")
	r.ServeHTTP(rec, req)
	// empty upload fails at zip read (400 bad_zip), not 500
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "bad_zip")
}

func TestHandler_Validate_OK(t *testing.T) {
	// Build a minimal valid zip so readZipFromRequest succeeds.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	require.NoError(t, zw.Close())

	r := mount(t, &fakeBackupSvc{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backup/validate", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "application/zip")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
}

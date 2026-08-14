package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
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

func TestHandler_ArchiveUploadPayloadTooLargeMapsTo413(t *testing.T) {
	for _, path := range []string{"/backup/validate", "/backup/restore?mode=skip"} {
		t.Run(path, func(t *testing.T) {
			r := mount(t, &fakeBackupSvc{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, errReader{
				err: &http.MaxBytesError{Limit: maxBackupBytes},
			})
			req.Header.Set("Content-Type", "application/zip")

			r.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
			assert.Contains(t, rec.Body.String(), `"code":"payload_too_large"`)
		})
	}
}

type blockingValidateService struct {
	fakeBackupSvc
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingValidateService) Validate(_ context.Context, _ authctx.UserID, _ *zip.Reader) (Validation, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return Validation{OK: true}, nil
}

type readTrackingBody struct {
	reader io.Reader
	reads  int
}

func (r *readTrackingBody) Read(p []byte) (int, error) {
	r.reads++
	return r.reader.Read(p)
}

func (r *readTrackingBody) Close() error { return nil }

type blockingArchiveService struct {
	fakeBackupSvc
	operation string
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
	mu        sync.Mutex
	calls     map[string]int
}

func (s *blockingArchiveService) wait(operation string) {
	s.mu.Lock()
	s.calls[operation]++
	s.mu.Unlock()
	if operation != s.operation {
		return
	}
	s.once.Do(func() { close(s.entered) })
	<-s.release
}

func (s *blockingArchiveService) Export(_ context.Context, _ authctx.UserID, w io.Writer, onCountsReady func(Counts) error) (ExportReport, error) {
	s.wait("export")
	if onCountsReady != nil {
		if err := onCountsReady(Counts{}); err != nil {
			return ExportReport{}, err
		}
	}
	_, _ = w.Write([]byte("PK\x03\x04"))
	return ExportReport{}, nil
}

func (s *blockingArchiveService) Validate(_ context.Context, _ authctx.UserID, _ *zip.Reader) (Validation, error) {
	s.wait("validate")
	return Validation{OK: true}, nil
}

func (s *blockingArchiveService) Restore(_ context.Context, _ authctx.UserID, _ *zip.Reader, mode ConflictMode) (RestoreReport, error) {
	s.wait("restore")
	return RestoreReport{Mode: mode}, nil
}

func (s *blockingArchiveService) callCount(operation string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[operation]
}

func archiveRequest(t *testing.T, operation string, body io.Reader) *http.Request {
	t.Helper()
	path := "/backup"
	if operation != "export" {
		path += "/" + operation
	}
	req := httptest.NewRequest(http.MethodPost, path, body)
	if operation != "export" {
		req.Header.Set("Content-Type", "application/zip")
	}
	return req
}

func TestHandler_ArchiveAdmissionIsSharedByExportValidateAndRestore(t *testing.T) {
	operations := []string{"export", "validate", "restore"}
	for _, holder := range operations {
		t.Run(holder+"_holds_slot", func(t *testing.T) {
			svc := &blockingArchiveService{
				operation: holder,
				entered:   make(chan struct{}),
				release:   make(chan struct{}),
				calls:     make(map[string]int),
			}
			h := NewHandler(svc, slog.Default())
			createCalls := 0
			h.createTemp = func() (*os.File, error) {
				createCalls++
				return os.CreateTemp(t.TempDir(), "foldex-backup-admission-*.zip")
			}
			router := chi.NewRouter()
			router.Use(authctxtest.Middleware(authctxtest.DefaultUser))
			router.Route("/backup", h.Mount)

			holderDone := make(chan struct{})
			go func() {
				defer close(holderDone)
				body := io.Reader(nil)
				if holder != "export" {
					body = bytes.NewReader(minimalZip(t))
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, archiveRequest(t, holder, body))
				assert.Equal(t, http.StatusOK, rec.Code)
			}()
			<-svc.entered

			createdBeforeRejections := createCalls
			for _, contender := range operations {
				tracked := &readTrackingBody{reader: bytes.NewReader(minimalZip(t))}
				var body io.Reader
				if contender != "export" {
					body = tracked
				}
				callsBefore := svc.callCount(contender)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, archiveRequest(t, contender, body))
				assert.Equal(t, http.StatusTooManyRequests, rec.Code, "%s must reject while %s is active", contender, holder)
				assert.Contains(t, rec.Body.String(), "backup_busy")
				assert.Equal(t, 0, tracked.reads, "rejection must precede body reads")
				assert.Equal(t, callsBefore, svc.callCount(contender), "rejection must precede service work")
			}
			assert.Equal(t, createdBeforeRejections, createCalls, "rejections must not create temp files")

			close(svc.release)
			<-holderDone
		})
	}
}

func TestHandler_ArchiveAdmissionRejectsBeforeTempSpool(t *testing.T) {
	tempDir := t.TempDir()
	svc := &blockingValidateService{entered: make(chan struct{}), release: make(chan struct{})}
	h := NewHandler(svc, slog.Default())
	createCalls := 0
	h.createTemp = func() (*os.File, error) {
		createCalls++
		return os.CreateTemp(tempDir, "foldex-backup-test-*.zip")
	}
	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r.Route("/backup", h.Mount)

	firstZip := minimalZip(t)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/backup/validate", bytes.NewReader(firstZip))
		req.Header.Set("Content-Type", "application/zip")
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}()
	<-svc.entered
	require.Equal(t, 1, createCalls)

	secondBody := &readTrackingBody{reader: bytes.NewReader(minimalZip(t))}
	secondRec := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, "/backup/restore", secondBody)
	secondReq.Header.Set("Content-Type", "application/zip")
	r.ServeHTTP(secondRec, secondReq)
	assert.Equal(t, http.StatusTooManyRequests, secondRec.Code)
	assert.Equal(t, "1", secondRec.Header().Get("Retry-After"))
	assert.Contains(t, secondRec.Body.String(), "backup_busy")
	assert.Equal(t, 0, secondBody.reads, "rejected request body must not be copied")
	assert.Equal(t, 1, createCalls, "rejected request must not create a temp file")

	close(svc.release)
	<-firstDone
	files, err := os.ReadDir(tempDir)
	require.NoError(t, err)
	assert.Empty(t, files, "admitted operation must remove its temp file")

	thirdRec := httptest.NewRecorder()
	thirdReq := httptest.NewRequest(http.MethodPost, "/backup/validate", bytes.NewReader(minimalZip(t)))
	thirdReq.Header.Set("Content-Type", "application/zip")
	r.ServeHTTP(thirdRec, thirdReq)
	assert.Equal(t, http.StatusOK, thirdRec.Code, "completed operation must release admission")
	assert.Equal(t, 2, createCalls)
	files, err = os.ReadDir(tempDir)
	require.NoError(t, err)
	assert.Empty(t, files, "subsequent operation must also clean up")
}

// compile-time check that fake still satisfies interface after edits
var _ BackupService = (*fakeBackupSvc)(nil)

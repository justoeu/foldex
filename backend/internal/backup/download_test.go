package backup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

type downloadBackupService struct {
	mu               sync.Mutex
	exportCalls      int
	exportUIDs       []authctx.UserID
	exportEntered    chan struct{}
	exportRelease    chan struct{}
	enterOnce        sync.Once
	exportErr        error
	failAfterHeaders bool
}

func (s *downloadBackupService) Export(_ context.Context, uid authctx.UserID, w io.Writer, onCountsReady func(Counts) error) (ExportReport, error) {
	s.mu.Lock()
	s.exportCalls++
	s.exportUIDs = append(s.exportUIDs, uid)
	exportEntered := s.exportEntered
	exportRelease := s.exportRelease
	exportErr := s.exportErr
	failAfterHeaders := s.failAfterHeaders
	s.mu.Unlock()
	if exportEntered != nil {
		s.enterOnce.Do(func() { close(exportEntered) })
	}
	if exportRelease != nil {
		<-exportRelease
	}
	counts := Counts{Links: 3, Files: 1, FileBytes: 9}
	rep := ExportReport{Counts: counts, DurationMs: 17}
	if exportErr != nil && !failAfterHeaders {
		return rep, exportErr
	}
	if err := onCountsReady(counts); err != nil {
		return ExportReport{}, err
	}
	_, _ = w.Write([]byte("PK\x03\x04"))
	if exportErr != nil {
		return rep, exportErr
	}
	return rep, nil
}

func (s *downloadBackupService) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exportCalls
}

func (s *downloadBackupService) uids() []authctx.UserID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authctx.UserID(nil), s.exportUIDs...)
}

func (*downloadBackupService) Validate(context.Context, authctx.UserID, *zip.Reader) (Validation, error) {
	return Validation{}, nil
}

func (*downloadBackupService) Restore(context.Context, authctx.UserID, *zip.Reader, ConflictMode) (RestoreReport, error) {
	return RestoreReport{}, nil
}

func requestAs(method, target string, p authctx.Principal) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	return req.WithContext(authctx.WithPrincipal(req.Context(), p))
}

func issueDownloadFor(t *testing.T, h *Handler, p authctx.Principal) issuedDownload {
	t.Helper()
	rec := httptest.NewRecorder()
	h.issueDownload(rec, requestAs(http.MethodPost, "/api/backup/download", p))
	require.Equal(t, http.StatusCreated, rec.Code)
	var issued issuedDownload
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&issued))
	require.NotEmpty(t, issued.ID)
	require.NotEmpty(t, issued.DownloadURL)
	return issued
}

func statusFor(t *testing.T, h *Handler, issued issuedDownload, p authctx.Principal) (int, downloadStatus) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.downloadStatus(rec, requestAs(http.MethodGet, issued.StatusURL, p))
	var status downloadStatus
	if rec.Code == http.StatusOK {
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&status))
	}
	return rec.Code, status
}

type headerWriteRecorder struct {
	*httptest.ResponseRecorder
	statuses []int
}

func (r *headerWriteRecorder) WriteHeader(status int) {
	r.statuses = append(r.statuses, status)
	r.ResponseRecorder.WriteHeader(status)
}

func assertFailedDownloadIsConsumed(t *testing.T, h *Handler, svc *downloadBackupService, issued issuedDownload, owner authctx.Principal) {
	t.Helper()
	assert.False(t, h.AllowsDownloadNavigation(httptest.NewRequest(http.MethodGet, issued.DownloadURL, nil)))

	replay := httptest.NewRecorder()
	h.download(replay, requestAs(http.MethodGet, issued.DownloadURL, owner))
	assert.Equal(t, http.StatusNotFound, replay.Code)
	assert.Equal(t, 1, svc.calls(), "a failed one-time download must not export again")

	code, status := statusFor(t, h, issued, owner)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, downloadFailed, status.State)
	require.NotNil(t, status.Failure)
	assert.Equal(t, "export_failed", status.Failure.Code)
	assert.Zero(t, status.SizeBytes)
	assert.Zero(t, status.DurationMs)
	assert.Equal(t, Counts{}, status.Counts, "failed exports must not record completion history")
}

func TestDownloadTicketIsSessionBoundOneTimeAndExportsOnce(t *testing.T) {
	svc := &downloadBackupService{}
	h := NewHandler(svc, slog.Default())
	owner := authctx.Principal{UserID: 7, SessionID: 41, Via: authctx.ViaSession}
	issued := issueDownloadFor(t, h, owner)

	parsed, err := url.Parse(issued.DownloadURL)
	require.NoError(t, err)
	assert.True(t, h.AllowsDownloadNavigation(httptest.NewRequest(http.MethodGet, issued.DownloadURL, nil)))

	for _, other := range []authctx.Principal{
		{UserID: 8, SessionID: 41, Via: authctx.ViaSession},
		{UserID: 7, SessionID: 42, Via: authctx.ViaSession},
	} {
		rec := httptest.NewRecorder()
		h.download(rec, requestAs(http.MethodGet, issued.DownloadURL, other))
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, 0, svc.calls())
	}
	assert.NotEmpty(t, parsed.Query().Get("token"))
	badQuery := parsed.Query()
	badQuery.Set("token", "not-the-ticket")
	parsed.RawQuery = badQuery.Encode()
	badToken := httptest.NewRecorder()
	h.download(badToken, requestAs(http.MethodGet, parsed.String(), owner))
	assert.Equal(t, http.StatusNotFound, badToken.Code)
	assert.Equal(t, 0, svc.calls())

	rec := httptest.NewRecorder()
	h.download(rec, requestAs(http.MethodGet, issued.DownloadURL, owner))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "PK\x03\x04", rec.Body.String())
	assert.Contains(t, rec.Header().Get("Content-Disposition"), issued.Filename)
	assert.Equal(t, []authctx.UserID{owner.UserID}, svc.uids())
	assert.False(t, h.AllowsDownloadNavigation(httptest.NewRequest(http.MethodGet, issued.DownloadURL, nil)))

	replay := httptest.NewRecorder()
	h.download(replay, requestAs(http.MethodGet, issued.DownloadURL, owner))
	assert.Equal(t, http.StatusNotFound, replay.Code)
	assert.Equal(t, 1, svc.calls(), "ticket replay must not generate a second archive")

	code, status := statusFor(t, h, issued, owner)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, downloadComplete, status.State)
	assert.Equal(t, int64(4), status.SizeBytes)
	assert.Equal(t, int64(17), status.DurationMs)
	assert.Equal(t, int64(3), status.Counts.Links)

	code, _ = statusFor(t, h, issued, authctx.Principal{UserID: 8, SessionID: 41, Via: authctx.ViaSession})
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = statusFor(t, h, issued, authctx.Principal{UserID: 7, SessionID: 99, Via: authctx.ViaSession})
	assert.Equal(t, http.StatusOK, code, "session refresh must not lose completed history metadata")
}

func TestDownloadTicketExportFailureBeforeHeadersIsFinal(t *testing.T) {
	svc := &downloadBackupService{exportErr: errors.New("snapshot failed")}
	h := NewHandler(svc, slog.Default())
	owner := authctx.Principal{UserID: 7, SessionID: 41, Via: authctx.ViaSession}
	issued := issueDownloadFor(t, h, owner)
	rec := &headerWriteRecorder{ResponseRecorder: httptest.NewRecorder()}

	h.download(rec, requestAs(http.MethodGet, issued.DownloadURL, owner))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, []int{http.StatusInternalServerError}, rec.statuses)
	assert.Contains(t, rec.Body.String(), `"code":"export_failed"`)
	assertFailedDownloadIsConsumed(t, h, svc, issued, owner)
}

func TestDownloadTicketExportFailureAfterHeadersTruncatesWithoutJSONRewrite(t *testing.T) {
	svc := &downloadBackupService{
		exportErr:        errors.New("archive write failed"),
		failAfterHeaders: true,
	}
	h := NewHandler(svc, slog.Default())
	owner := authctx.Principal{UserID: 7, SessionID: 41, Via: authctx.ViaSession}
	issued := issueDownloadFor(t, h, owner)
	rec := &headerWriteRecorder{ResponseRecorder: httptest.NewRecorder()}

	h.download(rec, requestAs(http.MethodGet, issued.DownloadURL, owner))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []int{http.StatusOK}, rec.statuses, "committed ZIP response must not attempt a second JSON status write")
	assert.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
	assert.Equal(t, "PK\x03\x04", rec.Body.String(), "post-header failure must leave only the truncated archive")
	assertFailedDownloadIsConsumed(t, h, svc, issued, owner)
}

func TestDownloadTicketExpiresClosed(t *testing.T) {
	svc := &downloadBackupService{}
	h := NewHandler(svc, slog.Default())
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	h.downloads.now = func() time.Time { return now }
	owner := authctx.Principal{UserID: 7, SessionID: 41, Via: authctx.ViaSession}
	issued := issueDownloadFor(t, h, owner)
	now = now.Add(downloadTicketTTL + time.Second)

	assert.False(t, h.AllowsDownloadNavigation(httptest.NewRequest(http.MethodGet, issued.DownloadURL, nil)))
	rec := httptest.NewRecorder()
	h.download(rec, requestAs(http.MethodGet, issued.DownloadURL, owner))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Zero(t, svc.calls())

	code, status := statusFor(t, h, issued, owner)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, downloadFailed, status.State)
	require.NotNil(t, status.Failure)
	assert.Equal(t, "download_expired", status.Failure.Code)
}

func TestDownloadTicketConsumesOnAdmissionRejection(t *testing.T) {
	svc := &downloadBackupService{}
	h := NewHandler(svc, slog.Default())
	owner := authctx.Principal{UserID: 7, SessionID: 41, Via: authctx.ViaSession}
	issued := issueDownloadFor(t, h, owner)
	h.archiveSlots <- struct{}{}

	rec := httptest.NewRecorder()
	h.download(rec, requestAs(http.MethodGet, issued.DownloadURL, owner))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Zero(t, svc.calls())
	<-h.archiveSlots

	replay := httptest.NewRecorder()
	h.download(replay, requestAs(http.MethodGet, issued.DownloadURL, owner))
	assert.Equal(t, http.StatusNotFound, replay.Code)
	assert.Zero(t, svc.calls())

	code, status := statusFor(t, h, issued, owner)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, downloadFailed, status.State)
	require.NotNil(t, status.Failure)
	assert.Equal(t, "backup_busy", status.Failure.Code)
}

func TestDownloadTicketConcurrentConsumptionExportsExactlyOnce(t *testing.T) {
	svc := &downloadBackupService{
		exportEntered: make(chan struct{}),
		exportRelease: make(chan struct{}),
	}
	h := NewHandler(svc, slog.Default())
	owner := authctx.Principal{UserID: 7, SessionID: 41, Via: authctx.ViaSession}
	issued := issueDownloadFor(t, h, owner)
	start := make(chan struct{})
	codes := make(chan int, 2)

	for range 2 {
		go func() {
			<-start
			rec := httptest.NewRecorder()
			h.download(rec, requestAs(http.MethodGet, issued.DownloadURL, owner))
			codes <- rec.Code
		}()
	}
	close(start)
	<-svc.exportEntered

	assert.Equal(t, http.StatusNotFound, <-codes)
	assert.Equal(t, 1, svc.calls())
	close(svc.exportRelease)
	assert.Equal(t, http.StatusOK, <-codes)
	assert.Equal(t, 1, svc.calls())
}

func TestDownloadTicketIssuanceReplacesPendingForExactSession(t *testing.T) {
	store := newDownloadTickets()
	owner := authctx.Principal{UserID: 7, SessionID: 41, Via: authctx.ViaSession}
	first, err := store.issue(owner)
	require.NoError(t, err)
	second, err := store.issue(owner)
	require.NoError(t, err)

	firstURL, err := url.Parse(first.DownloadURL)
	require.NoError(t, err)
	secondURL, err := url.Parse(second.DownloadURL)
	require.NoError(t, err)
	assert.False(t, store.allows(first.ID, firstURL.Query().Get("token")))
	assert.True(t, store.allows(second.ID, secondURL.Query().Get("token")))
	assert.Len(t, store.tickets, 1)
}

func TestDownloadTicketPerUserActiveLimitLeavesCapacityForOthers(t *testing.T) {
	store := newDownloadTickets()
	owner := authctx.Principal{UserID: 7, SessionID: 1, Via: authctx.ViaSession}
	first, err := store.issue(owner)
	require.NoError(t, err)
	firstURL, err := url.Parse(first.DownloadURL)
	require.NoError(t, err)
	_, ok := store.consume(first.ID, firstURL.Query().Get("token"), owner)
	require.True(t, ok)

	for sessionID := int64(2); sessionID <= maxActiveDownloadTicketsPerUser; sessionID++ {
		_, err = store.issue(authctx.Principal{UserID: owner.UserID, SessionID: sessionID, Via: authctx.ViaSession})
		require.NoError(t, err)
	}
	_, err = store.issue(authctx.Principal{UserID: owner.UserID, SessionID: 99, Via: authctx.ViaSession})
	assert.ErrorIs(t, err, errUserDownloadTicketsFull)
	_, err = store.issue(authctx.Principal{UserID: 8, SessionID: 1, Via: authctx.ViaSession})
	require.NoError(t, err, "one user must not consume the global ticket budget")
}

func TestDownloadTicketPerUserLimitMapsTo429(t *testing.T) {
	h := NewHandler(&downloadBackupService{}, slog.Default())
	for sessionID := int64(1); sessionID <= maxActiveDownloadTicketsPerUser; sessionID++ {
		issueDownloadFor(t, h, authctx.Principal{UserID: 7, SessionID: sessionID, Via: authctx.ViaSession})
	}

	rec := httptest.NewRecorder()
	h.issueDownload(rec, requestAs(http.MethodPost, "/api/backup/download",
		authctx.Principal{UserID: 7, SessionID: 99, Via: authctx.ViaSession}))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), "download_ticket_limit")
}

func TestDownloadTicketFinishedResultRetention(t *testing.T) {
	store := newDownloadTickets()
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	owner := authctx.Principal{UserID: 7, SessionID: 41, Via: authctx.ViaSession}
	issued, err := store.issue(owner)
	require.NoError(t, err)
	downloadURL, err := url.Parse(issued.DownloadURL)
	require.NoError(t, err)
	_, ok := store.consume(issued.ID, downloadURL.Query().Get("token"), owner)
	require.True(t, ok)
	store.complete(issued.ID, ExportReport{Counts: Counts{Links: 1}}, 123)

	now = now.Add(downloadResultRetention - time.Second)
	status, ok := store.status(issued.ID, owner)
	require.True(t, ok)
	assert.Equal(t, downloadComplete, status.State)
	now = now.Add(2 * time.Second)
	_, ok = store.status(issued.ID, owner)
	assert.False(t, ok)
}

func TestDownloadTicketStateIsBoundedAndPruned(t *testing.T) {
	store := newDownloadTickets()
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for i := range maxDownloadTickets {
		owner := authctx.Principal{
			UserID:    authctx.UserID(i/maxActiveDownloadTicketsPerUser + 1),
			SessionID: int64(i + 1),
			Via:       authctx.ViaSession,
		}
		_, err := store.issue(owner)
		require.NoError(t, err)
	}
	owner := authctx.Principal{UserID: 999, SessionID: 1, Via: authctx.ViaSession}
	_, err := store.issue(owner)
	assert.ErrorIs(t, err, errDownloadTicketsFull)

	now = now.Add(downloadTicketTTL + time.Second)
	_, err = store.issue(owner)
	require.NoError(t, err, "expired tickets must make room for a new download")
	assert.Len(t, store.tickets, maxDownloadTickets)

	now = now.Add(downloadResultRetention + downloadTicketTTL + time.Second)
	store.mu.Lock()
	store.cleanupLocked(now)
	remaining := len(store.tickets)
	store.mu.Unlock()
	assert.Equal(t, 1, remaining, "completed/failed results must be explicitly pruned")
}

package backup

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/secrets"
)

const (
	downloadTicketTTL               = time.Minute
	downloadResultRetention         = 10 * time.Minute
	maxDownloadTickets              = 128
	maxActiveDownloadTicketsPerUser = 4
)

var (
	errDownloadTicketsFull     = errors.New("backup: too many download tickets")
	errUserDownloadTicketsFull = errors.New("backup: too many active download tickets for user")
)

type downloadState string

const (
	downloadPending  downloadState = "pending"
	downloadRunning  downloadState = "running"
	downloadComplete downloadState = "complete"
	downloadFailed   downloadState = "failed"
)

type downloadFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type downloadTicket struct {
	id         string
	tokenHash  []byte
	userID     authctx.UserID
	sessionID  int64
	createdAt  time.Time
	expiresAt  time.Time
	finishedAt time.Time
	state      downloadState
	counts     Counts
	sizeBytes  int64
	durationMs int64
	failure    *downloadFailure
}

type issuedDownload struct {
	ID          string    `json:"id"`
	DownloadURL string    `json:"download_url"`
	StatusURL   string    `json:"status_url"`
	Filename    string    `json:"filename"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type downloadStatus struct {
	ID         string           `json:"id"`
	State      downloadState    `json:"state"`
	CreatedAt  time.Time        `json:"created_at"`
	DurationMs int64            `json:"duration_ms"`
	SizeBytes  int64            `json:"size_bytes"`
	Counts     Counts           `json:"counts"`
	Failure    *downloadFailure `json:"error,omitempty"`
}

type downloadTickets struct {
	mu      sync.Mutex
	tickets map[string]*downloadTicket
	now     func() time.Time
}

func newDownloadTickets() *downloadTickets {
	return &downloadTickets{
		tickets: make(map[string]*downloadTicket),
		now:     time.Now,
	}
}

func (s *downloadTickets) issue(p authctx.Principal) (issuedDownload, error) {
	id, _, err := secrets.NewToken()
	if err != nil {
		return issuedDownload{}, err
	}
	rawToken, tokenHash, err := secrets.NewToken()
	if err != nil {
		return issuedDownload{}, err
	}
	now := s.now().UTC()
	ticket := &downloadTicket{
		id:        id,
		tokenHash: tokenHash,
		userID:    p.UserID,
		sessionID: p.SessionID,
		createdAt: now,
		expiresAt: now.Add(downloadTicketTTL),
		state:     downloadPending,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	if _, exists := s.tickets[id]; exists {
		return issuedDownload{}, errors.New("backup: duplicate download ticket id")
	}
	activeForUser := 0
	for existingID, existing := range s.tickets {
		if existing.userID == p.UserID && existing.sessionID == p.SessionID && existing.state == downloadPending {
			delete(s.tickets, existingID)
			continue
		}
		if existing.userID == p.UserID && (existing.state == downloadPending || existing.state == downloadRunning) {
			activeForUser++
		}
	}
	if activeForUser >= maxActiveDownloadTicketsPerUser {
		return issuedDownload{}, errUserDownloadTicketsFull
	}
	s.evictFinishedLocked()
	if len(s.tickets) >= maxDownloadTickets {
		return issuedDownload{}, errDownloadTicketsFull
	}
	s.tickets[id] = ticket

	query := url.Values{"id": {id}, "token": {rawToken}}
	statusQuery := url.Values{"id": {id}}
	return issuedDownload{
		ID:          id,
		DownloadURL: "/api/backup/download?" + query.Encode(),
		StatusURL:   "/api/backup/download/status?" + statusQuery.Encode(),
		Filename:    filenameForBackup(now),
		CreatedAt:   now,
		ExpiresAt:   ticket.expiresAt,
	}, nil
}

func (s *downloadTickets) evictFinishedLocked() {
	for len(s.tickets) >= maxDownloadTickets {
		var oldestID string
		var oldest time.Time
		for id, ticket := range s.tickets {
			if ticket.state != downloadComplete && ticket.state != downloadFailed {
				continue
			}
			if oldestID == "" || ticket.finishedAt.Before(oldest) {
				oldestID = id
				oldest = ticket.finishedAt
			}
		}
		if oldestID == "" {
			return
		}
		delete(s.tickets, oldestID)
	}
}

func (s *downloadTickets) consume(id, rawToken string, p authctx.Principal) (*downloadTicket, bool) {
	if id == "" || rawToken == "" {
		return nil, false
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	ticket, ok := s.tickets[id]
	if !ok || ticket.state != downloadPending || !now.Before(ticket.expiresAt) {
		return nil, false
	}
	if !secrets.Equal(ticket.tokenHash, secrets.Hash(rawToken)) ||
		ticket.userID != p.UserID || ticket.sessionID != p.SessionID {
		return nil, false
	}
	ticket.state = downloadRunning
	return ticket, true
}

func (s *downloadTickets) complete(id string, rep ExportReport, sizeBytes int64) {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if ticket := s.tickets[id]; ticket != nil && ticket.state == downloadRunning {
		ticket.state = downloadComplete
		ticket.counts = rep.Counts
		ticket.sizeBytes = sizeBytes
		ticket.durationMs = rep.DurationMs
		ticket.finishedAt = now
	}
}

func (s *downloadTickets) fail(id, code, message string) {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if ticket := s.tickets[id]; ticket != nil && (ticket.state == downloadPending || ticket.state == downloadRunning) {
		ticket.state = downloadFailed
		ticket.failure = &downloadFailure{Code: code, Message: message}
		ticket.finishedAt = now
	}
}

func (s *downloadTickets) status(id string, p authctx.Principal) (downloadStatus, bool) {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	ticket, ok := s.tickets[id]
	// Export may outlive the 15-minute access cookie. The download capability
	// itself stays bound to the exact issuing session; status exposes only the
	// owner's counts/size so refresh rotation cannot erase useful history.
	if !ok || ticket.userID != p.UserID {
		return downloadStatus{}, false
	}
	return downloadStatus{
		ID:         ticket.id,
		State:      ticket.state,
		CreatedAt:  ticket.createdAt,
		DurationMs: ticket.durationMs,
		SizeBytes:  ticket.sizeBytes,
		Counts:     ticket.counts,
		Failure:    ticket.failure,
	}, true
}

func (s *downloadTickets) cleanupLocked(now time.Time) {
	for id, ticket := range s.tickets {
		if ticket.state == downloadPending && !now.Before(ticket.expiresAt) {
			ticket.state = downloadFailed
			ticket.failure = &downloadFailure{Code: "download_expired", Message: "backup download expired before it started"}
			ticket.finishedAt = now
		}
		if (ticket.state == downloadComplete || ticket.state == downloadFailed) &&
			!ticket.finishedAt.IsZero() && !now.Before(ticket.finishedAt.Add(downloadResultRetention)) {
			delete(s.tickets, id)
		}
	}
}

func (h *Handler) issueDownload(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(r)
	issued, err := h.downloads.issue(p)
	if err != nil {
		if errors.Is(err, errDownloadTicketsFull) || errors.Is(err, errUserDownloadTicketsFull) {
			w.Header().Set("Retry-After", "1")
			httperr.Write(w, httperr.New(http.StatusTooManyRequests, "download_ticket_limit", "too many active backup downloads"))
			return
		}
		h.logger.Error("backup download ticket issuance failed", "err", err)
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "download_ticket_failed", "failed to prepare backup download"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httperr.JSON(w, http.StatusCreated, issued)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(r)
	ticket, ok := h.downloads.consume(r.URL.Query().Get("id"), r.URL.Query().Get("token"), p)
	if !ok {
		httperr.Write(w, httperr.New(http.StatusNotFound, "download_invalid", "backup download is invalid or expired"))
		return
	}
	completed := false
	defer func() {
		if !completed {
			h.downloads.fail(ticket.id, "export_failed", "failed to produce backup")
		}
	}()

	release, admitted := h.admitArchive(w)
	if !admitted {
		h.downloads.fail(ticket.id, "backup_busy", "another backup archive operation is in progress")
		completed = true
		return
	}
	defer release()

	rep, size, headersWritten, err := h.writeExport(w, r, ticket.createdAt)
	if err != nil {
		h.downloads.fail(ticket.id, "export_failed", "failed to produce backup")
		h.writeExportError(w, err, headersWritten)
		completed = true
		return
	}
	h.downloads.complete(ticket.id, rep, size)
	h.logExportOK(filenameForBackup(ticket.createdAt), rep)
	completed = true
}

func (h *Handler) downloadStatus(w http.ResponseWriter, r *http.Request) {
	status, ok := h.downloads.status(r.URL.Query().Get("id"), mustPrincipal(r))
	if !ok {
		httperr.Write(w, httperr.New(http.StatusNotFound, "download_not_found", "backup download not found"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httperr.JSON(w, http.StatusOK, status)
}

func mustPrincipal(r *http.Request) authctx.Principal {
	p, ok := authctx.FromContext(r.Context())
	if !ok {
		panic("backup: route is mounted outside authenticated principal middleware")
	}
	return p
}

func filenameForBackup(createdAt time.Time) string {
	return fmt.Sprintf("foldex-backup-%s.zip", createdAt.UTC().Format("20060102T150405Z"))
}

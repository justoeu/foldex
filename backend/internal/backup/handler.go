package backup

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/roleperm"
)

// BackupService is the application port used by HTTP handlers (testable fake).
type BackupService interface {
	Export(ctx context.Context, uid authctx.UserID, w io.Writer, onCountsReady func(Counts) error) (ExportReport, error)
	Validate(ctx context.Context, uid authctx.UserID, zr *zip.Reader) (Validation, error)
	Restore(ctx context.Context, uid authctx.UserID, zr *zip.Reader, mode ConflictMode) (RestoreReport, error)
}

type Handler struct {
	svc          BackupService
	logger       *slog.Logger
	archiveSlots chan struct{}
	createTemp   func() (*os.File, error)
	downloads    *downloadTickets
	// grants is the effective RBAC matrix (ADR-42). Nil at construction means
	// the compiled one — what every test that does not care about configured
	// permissions wants; main always passes the loaded repository.
	grants authgate.Grants
}

const maxConcurrentArchiveOperations = 1

const archiveRequestTimeout = 31 * time.Minute

func NewHandler(svc BackupService, logger *slog.Logger, grants authgate.Grants) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	grants = roleperm.OrDefault(grants)
	return &Handler{
		svc:          svc,
		logger:       logger,
		grants:       grants,
		archiveSlots: make(chan struct{}, maxConcurrentArchiveOperations),
		downloads:    newDownloadTickets(),
		createTemp: func() (*os.File, error) {
			return os.CreateTemp("", "foldex-backup-*.zip")
		},
	}
}

func (h *Handler) Mount(r chi.Router) {
	// Export and validate only READ — they serialize rows the caller already
	// owns and inspect an archive without applying it — which is why a viewer
	// holds backup.export by default. But reading is not the same as reading
	// OUT: the archive leaves the instance, and an owner who unticks
	// backup.export is saying exactly that. Ungated, that tick saved, audited
	// and did nothing (ADR-42).
	export := authgate.RequirePermission(h.grants, authctx.PermBackupExport)
	r.With(export).Post("/", h.export)
	r.With(export).Post("/download", h.issueDownload)
	r.With(export).Get("/download", h.download)
	r.With(export).Get("/download/status", h.downloadStatus)
	r.With(export).Post("/validate", h.validate)
	// Restore is the one route here that writes.
	r.With(authgate.RequirePermission(h.grants, authctx.PermBackupRestore)).Post("/restore", h.restore)
}

// ────────────────────────────────────────────────────────────────────────────
// POST /api/backup — stream ZIP

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	release, ok := h.admitArchive(w)
	if !ok {
		return
	}
	defer release()

	createdAt := time.Now().UTC()
	filename := filenameForBackup(createdAt)
	rep, _, headersWritten, err := h.writeExport(w, r, createdAt)
	if err != nil {
		h.writeExportError(w, err, headersWritten)
		return
	}
	h.logExportOK(filename, rep)
}

type countingWriter struct {
	w       io.Writer
	written int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.written += int64(n)
	return n, err
}

func (h *Handler) writeExport(w http.ResponseWriter, r *http.Request, createdAt time.Time) (ExportReport, int64, bool, error) {
	extendArchiveDeadlines(w)
	filename := filenameForBackup(createdAt)
	// Streaming export. The Service computes counts up front (snapshot read
	// + bucket listings under REPEATABLE READ) and calls onCountsReady
	// BEFORE the first zip byte; the hook flushes response headers, then
	// every entry streams straight to w. X-Foldex-Backup-Duration-Ms used to
	// land in the headers but the duration is only known after the zip is
	// closed — clients that need it can derive from request start.
	headersWritten := false
	counter := &countingWriter{w: w}
	rep, err := h.svc.Export(r.Context(), authctx.MustUser(r.Context()), counter, func(c Counts) error {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Foldex-Backup-Filename", filename)
		// Full count set so the SPA can record history without buffering the
		// entire zip twice (blob + arrayBuffer) just to walk the EOCD.
		w.Header().Set("X-Foldex-Backup-Counts-Links", fmt.Sprintf("%d", c.Links))
		w.Header().Set("X-Foldex-Backup-Counts-Notes", fmt.Sprintf("%d", c.Notes))
		w.Header().Set("X-Foldex-Backup-Counts-Tags", fmt.Sprintf("%d", c.Tags))
		w.Header().Set("X-Foldex-Backup-Counts-Folders", fmt.Sprintf("%d", c.Folders))
		w.Header().Set("X-Foldex-Backup-Counts-Link-Tags", fmt.Sprintf("%d", c.LinkTags))
		w.Header().Set("X-Foldex-Backup-Counts-Click-Logs", fmt.Sprintf("%d", c.ClickLogs))
		w.Header().Set("X-Foldex-Backup-Counts-Files", fmt.Sprintf("%d", c.Files))
		w.Header().Set("X-Foldex-Backup-Counts-File-Bytes", fmt.Sprintf("%d", c.FileBytes))
		w.WriteHeader(http.StatusOK)
		headersWritten = true
		return nil
	})
	return rep, counter.written, headersWritten, err
}

func (h *Handler) writeExportError(w http.ResponseWriter, err error, headersWritten bool) {
	// Once headers ship, truncating the stream is the only honest response;
	// the browser will reject the incomplete ZIP.
	h.logger.Error("backup export failed", "err", err, "headers_written", headersWritten)
	if !headersWritten {
		httperr.Write(w, httperr.New(http.StatusInternalServerError, "export_failed", "failed to produce backup"))
	}
}

func (h *Handler) logExportOK(filename string, rep ExportReport) {
	h.logger.Info("backup export ok",
		"filename", filename,
		"links", rep.Counts.Links, "notes", rep.Counts.Notes, "files", rep.Counts.Files,
		"duration_ms", rep.DurationMs,
	)
}

// ────────────────────────────────────────────────────────────────────────────
// POST /api/backup/validate

func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	release, ok := h.admitArchive(w)
	if !ok {
		return
	}
	defer release()
	extendArchiveDeadlines(w)
	zr, cleanup, err := readZipFromRequest(w, r, h.createTemp)
	if err != nil {
		if errors.Is(err, ErrPayloadTooLarge) {
			httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "payload_too_large", err.Error()))
			return
		}
		httperr.Write(w, httperr.New(http.StatusBadRequest, "bad_zip", err.Error()))
		return
	}
	defer cleanup()
	v, err := h.svc.Validate(r.Context(), authctx.MustUser(r.Context()), zr)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, v)
}

// ────────────────────────────────────────────────────────────────────────────
// POST /api/backup/restore?mode=…

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	modeStr := r.URL.Query().Get("mode")
	if modeStr == "" {
		modeStr = string(ModeSkip)
	}
	mode := ConflictMode(modeStr)
	if !mode.Valid() {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "bad_mode", fmt.Sprintf("mode %q is not one of wipe|skip|duplicate", modeStr)))
		return
	}

	release, ok := h.admitArchive(w)
	if !ok {
		return
	}
	defer release()
	extendArchiveDeadlines(w)
	zr, cleanup, err := readZipFromRequest(w, r, h.createTemp)
	if err != nil {
		if errors.Is(err, ErrPayloadTooLarge) {
			httperr.Write(w, httperr.New(http.StatusRequestEntityTooLarge, "payload_too_large", err.Error()))
			return
		}
		httperr.Write(w, httperr.New(http.StatusBadRequest, "bad_zip", err.Error()))
		return
	}
	defer cleanup()
	rep, err := h.svc.Restore(r.Context(), authctx.MustUser(r.Context()), zr, mode)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, rep)
}

func extendArchiveDeadlines(w http.ResponseWriter) {
	deadline := time.Now().Add(archiveRequestTimeout)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
}

// ────────────────────────────────────────────────────────────────────────────
// readZipFromRequest streams either a raw application/zip body or a multipart
// upload with a `file` field to a temp file on disk, then opens it as a
// zip.Reader (which only needs a ReaderAt). Streaming to disk keeps heap usage
// bounded at O(1) regardless of backup size — a multi-GB upload used to
// allocate the same multi-GB on the heap.

const maxBackupBytes = int64(2 << 30) // 2 GiB

func (h *Handler) admitArchive(w http.ResponseWriter) (func(), bool) {
	select {
	case h.archiveSlots <- struct{}{}:
		return func() { <-h.archiveSlots }, true
	default:
		w.Header().Set("Retry-After", "1")
		httperr.Write(w, httperr.New(http.StatusTooManyRequests, "backup_busy", "another backup archive operation is in progress"))
		return func() {}, false
	}
}

func readZipFromRequest(w http.ResponseWriter, r *http.Request, createTemp func() (*os.File, error)) (*zip.Reader, func(), error) {
	ct := r.Header.Get("Content-Type")
	noop := func() {}

	// Hard cap on the entire request body, regardless of transport (raw zip
	// or multipart). Applies to both branches below — multipart parts that
	// would individually pass maxBackupBytes still trip this when summed.
	// Passing the real ResponseWriter (not nil) lets the cap surface as a
	// 413 instead of a 500 when streamToTempZip wraps the limit error.
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupBytes)

	if strings.HasPrefix(ct, "application/zip") {
		return streamToTempZipWith(r.Body, createTemp)
	}

	// multipart/form-data
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, noop, fmt.Errorf("expected application/zip or multipart/form-data: %w", err)
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, noop, fmt.Errorf("multipart read: %w", err)
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		defer part.Close()
		return streamToTempZipWith(io.LimitReader(part, maxBackupBytes), createTemp)
	}
	return nil, noop, fmt.Errorf("no `file` part in multipart upload")
}

// ErrPayloadTooLarge is returned by streamToTempZip when the body exceeded
// maxBackupBytes. Callers map it to 413 instead of a generic 500.
var ErrPayloadTooLarge = fmt.Errorf("backup: upload exceeds %d-byte limit", maxBackupBytes)

// streamToTempZip copies src to a temp file, opens it as a zip.Reader, and
// returns a cleanup that closes + removes the temp file. The temp file lives
// only for the duration of the restore — successful and failed paths both go
// through the cleanup closure. Permissions default to 0600 via os.CreateTemp.
func streamToTempZip(src io.Reader) (*zip.Reader, func(), error) {
	return streamToTempZipWith(src, func() (*os.File, error) {
		return os.CreateTemp("", "foldex-backup-*.zip")
	})
}

func streamToTempZipWith(src io.Reader, createTemp func() (*os.File, error)) (*zip.Reader, func(), error) {
	tmp, err := createTemp()
	if err != nil {
		return nil, func() {}, fmt.Errorf("create temp: %w", err)
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	n, err := io.Copy(tmp, src)
	if err != nil {
		cleanup()
		// http.MaxBytesError signals the body cap was tripped — surface as a
		// typed sentinel so the handler can return 413 instead of 500.
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, func() {}, ErrPayloadTooLarge
		}
		return nil, func() {}, fmt.Errorf("copy upload to temp: %w", err)
	}
	if n == 0 {
		cleanup()
		return nil, func() {}, fmt.Errorf("upload is empty")
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("parse zip: %w", err)
	}
	return zr, cleanup, nil
}

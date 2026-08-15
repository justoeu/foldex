package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

type restoreTransactionResult struct {
	report    RestoreReport
	mapping   idMapping
	ownedKeys []string
	ledger    *restoreLedger
}

func (s *Service) Restore(ctx context.Context, uid authctx.UserID, zr *zip.Reader, mode ConflictMode) (RestoreReport, error) {
	start := time.Now()
	empty := RestoreReport{Mode: mode, Warnings: []string{}}
	if !mode.Valid() {
		return empty, fmt.Errorf("backup: invalid mode %q", mode)
	}
	preflight, err := preflightRestore(ctx, zr)
	if err != nil {
		return empty, err
	}
	if report, found, err := s.restoreFromLedger(ctx, uid, zr, mode, preflight.archive.digest, start); found || err != nil {
		return report, err
	}

	prepared, err := prepareNoteMediaRestore(ctx, preflight.snapshot, zr)
	if err != nil {
		return empty, err
	}
	defer prepared.cleanup()

	result, err := s.applyRestoreTransaction(ctx, uid, mode, preflight, prepared)
	if err != nil {
		return empty, err
	}
	if result.ledger != nil {
		return s.resumeSkipRestore(ctx, uid, zr, preflight.archive.digest, *result.ledger, start, prepared)
	}
	return s.finishRestoreFiles(ctx, uid, zr, mode, preflight.archive.digest, result, prepared, start)
}

func preflightRestore(ctx context.Context, zr *zip.Reader) (backupArchiveInspection, error) {
	preflight := inspectBackupArchive(ctx, zr)
	if err := ctx.Err(); err != nil {
		return preflight, err
	}
	if len(preflight.errors) == 0 {
		return preflight, nil
	}
	message := strings.Join(preflight.errors, "; ")
	if preflight.manifest != nil && preflight.manifest.Kind != ManifestKind {
		message = "backup is not a foldex backup"
	}
	status := http.StatusBadRequest
	if preflight.unprocessable {
		status = http.StatusUnprocessableEntity
	}
	return preflight, httperr.New(status, "invalid_backup", message)
}

func (s *Service) restoreFromLedger(ctx context.Context, uid authctx.UserID, zr *zip.Reader, mode ConflictMode, digest [sha256.Size]byte, start time.Time) (RestoreReport, bool, error) {
	if mode != ModeSkip {
		return RestoreReport{}, false, nil
	}
	ledger, found, err := loadRestoreLedger(ctx, s.pool, uid, digest, mode)
	if err != nil || !found {
		return RestoreReport{}, found, err
	}
	report, err := s.resumeSkipRestore(ctx, uid, zr, digest, ledger, start, nil)
	return report, true, err
}

func (s *Service) applyRestoreTransaction(ctx context.Context, uid authctx.UserID, mode ConflictMode, preflight backupArchiveInspection, prepared *preparedNoteMediaRestore) (restoreTransactionResult, error) {
	result := restoreTransactionResult{report: RestoreReport{Mode: mode, Warnings: append([]string(nil), preflight.warnings...)}}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, fmt.Errorf("backup: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := lockRestoreTransaction(ctx, tx); err != nil {
		return result, err
	}
	ledger, found, err := transactionalRestoreLedger(ctx, tx, uid, preflight.archive.digest, mode)
	if err != nil {
		return result, err
	}
	if found {
		result.ledger = &ledger
		return result, nil
	}
	if mode == ModeWipe {
		result.ownedKeys, err = userObjectKeys(ctx, tx, uid, true)
		if err != nil {
			return result, fmt.Errorf("backup: enumerate own objects: %w", err)
		}
	}
	appendRestoreSnapshotWarnings(ctx, tx, uid, preflight.snapshot, &result.report)
	result.mapping, err = applyRestoreMode(ctx, tx, uid, mode, preflight.snapshot, &result.report)
	if err != nil {
		return result, err
	}
	result.mapping.noteFiles = prepared.mapping
	if err := finalizeRestoreTransaction(ctx, tx, uid, mode, preflight.archive.digest, preflight.snapshot, &result); err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("backup: commit: %w", err)
	}
	return result, nil
}

func lockRestoreTransaction(ctx context.Context, tx pgx.Tx) error {
	var acquired bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, RestoreAdvisoryLockKey).Scan(&acquired); err != nil {
		return fmt.Errorf("backup: advisory lock: %w", err)
	}
	if !acquired {
		return httperr.New(http.StatusConflict, "restore_in_progress", "another restore is already in progress")
	}
	return nil
}

func transactionalRestoreLedger(ctx context.Context, tx pgx.Tx, uid authctx.UserID, digest [sha256.Size]byte, mode ConflictMode) (restoreLedger, bool, error) {
	if mode != ModeSkip {
		return restoreLedger{}, false, nil
	}
	return loadRestoreLedger(ctx, tx, uid, digest, mode)
}

func appendRestoreSnapshotWarnings(ctx context.Context, tx pgx.Tx, uid authctx.UserID, snapshot *Snapshot, report *RestoreReport) {
	if snapshot.OwnerEmail != "" {
		var email string
		if err := tx.QueryRow(ctx, `SELECT email FROM app_user WHERE id = $1`, int64(uid)).Scan(&email); err == nil && email != snapshot.OwnerEmail {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"backup foi exportado por %q e está sendo restaurado na conta %q — todo o conteúdo passa a pertencer a %q",
				snapshot.OwnerEmail, email, email))
		}
	}
	if len(snapshot.AppSettings) > 0 {
		report.Warnings = append(report.Warnings,
			"configurações globais do backup foram ignoradas: a senha master agora é por usuário e não trafega no backup")
	}
}

func applyRestoreMode(ctx context.Context, tx pgx.Tx, uid authctx.UserID, mode ConflictMode, snapshot *Snapshot, report *RestoreReport) (idMapping, error) {
	switch mode {
	case ModeWipe:
		return applyWipeRestore(ctx, tx, uid, snapshot, report)
	case ModeSkip:
		inserted, skipped, mapping, err := restoreSkip(ctx, tx, uid, snapshot)
		report.Inserted, report.Skipped = inserted, skipped
		return mapping, wrapRestoreInsertError("skip", err)
	case ModeDuplicate:
		inserted, warnings, mapping, err := restoreDuplicate(ctx, tx, uid, snapshot)
		report.Inserted = inserted
		report.Warnings = append(report.Warnings, warnings...)
		return mapping, wrapRestoreInsertError("duplicate", err)
	default:
		return idMapping{}, fmt.Errorf("backup: invalid mode %q", mode)
	}
}

func applyWipeRestore(ctx context.Context, tx pgx.Tx, uid authctx.UserID, snapshot *Snapshot, report *RestoreReport) (idMapping, error) {
	if err := clearRestoreLedgers(ctx, tx, uid); err != nil {
		return idMapping{}, fmt.Errorf("backup: clear prior restore ledgers: %w", err)
	}
	wiped, err := wipeUser(ctx, tx, uid)
	if err != nil {
		return idMapping{}, fmt.Errorf("backup: wipe db: %w", err)
	}
	report.Wiped = wiped
	inserted, _, mapping, err := restoreSkip(ctx, tx, uid, snapshot)
	report.Inserted = inserted
	return mapping, wrapRestoreInsertError("wipe", err)
}

func wrapRestoreInsertError(mode string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("backup: insert (%s): %w", mode, err)
}

func finalizeRestoreTransaction(ctx context.Context, tx pgx.Tx, uid authctx.UserID, mode ConflictMode, digest [sha256.Size]byte, snapshot *Snapshot, result *restoreTransactionResult) error {
	if err := realignLinkImageURLs(ctx, tx, uid, result.mapping); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := restoreNoteMediaRefs(ctx, tx, uid, snapshot, result.mapping); err != nil {
		return fmt.Errorf("backup: restore note media refs: %w", err)
	}
	if mode == ModeSkip {
		if err := saveRestoreLedger(ctx, tx, uid, digest, mode, result.report, result.mapping); err != nil {
			return fmt.Errorf("backup: save restore ledger: %w", err)
		}
	}
	return nil
}

func (s *Service) finishRestoreFiles(ctx context.Context, uid authctx.UserID, zr *zip.Reader, mode ConflictMode, digest [sha256.Size]byte, result restoreTransactionResult, prepared *preparedNoteMediaRestore, start time.Time) (RestoreReport, error) {
	files, err := s.applyFiles(ctx, uid, zr, result.mapping, mode, result.ownedKeys, prepared)
	if err != nil {
		return result.report, fmt.Errorf("backup: files: %w", err)
	}
	result.report.Files = files
	if mode == ModeSkip {
		if err := completeRestoreLedger(ctx, s.pool, uid, digest, mode, files); err != nil {
			return result.report, fmt.Errorf("backup: %w", err)
		}
	}
	result.report.DurationMs = time.Since(start).Milliseconds()
	return result.report, nil
}

func (s *Service) resumeSkipRestore(ctx context.Context, uid authctx.UserID, zr *zip.Reader, digest [sha256.Size]byte, ledger restoreLedger, start time.Time, prepared *preparedNoteMediaRestore) (RestoreReport, error) {
	report := RestoreReport{
		Mode:     ModeSkip,
		Inserted: ledger.inserted,
		Skipped:  ledger.skipped,
		Warnings: append([]string(nil), ledger.warnings...),
		Files:    ledger.files,
	}
	if ledger.complete {
		report.DurationMs = time.Since(start).Milliseconds()
		return report, nil
	}
	files, err := s.applyFiles(ctx, uid, zr, ledger.mapping, ModeSkip, nil, prepared)
	if err != nil {
		return report, fmt.Errorf("backup: files: %w", err)
	}
	if err := completeRestoreLedger(ctx, s.pool, uid, digest, ModeSkip, files); err != nil {
		return report, fmt.Errorf("backup: %w", err)
	}
	report.Files = files
	report.DurationMs = time.Since(start).Milliseconds()
	return report, nil
}

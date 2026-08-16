package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

type restoreFileWork struct {
	entry        *zip.File
	key          string
	isNote       bool
	preparedFile preparedNoteMediaFile
	hasPrepared  bool
}

type restoreFilePlan struct {
	work    []restoreFileWork
	skipped int64
}

func (s *Service) applyFiles(ctx context.Context, uid authctx.UserID, zr *zip.Reader, mapping idMapping, mode ConflictMode, ownedKeys []string, prepared *preparedNoteMediaRestore) (FileReport, error) {
	plan, err := buildRestoreFilePlan(zr, mapping, prepared)
	if err != nil {
		return FileReport{}, err
	}
	report := FileReport{Skipped: plan.skipped}
	if err := s.wipeRestoreFiles(ctx, uid, mode, ownedKeys, &report); err != nil {
		return report, err
	}
	existing, err := s.existingRestoreFiles(ctx, mode, plan.work)
	if err != nil {
		return report, err
	}
	results, err := runRestoreObjectTasks(ctx, s.buildRestoreObjectTasks(plan.work, existing, prepared))
	if err != nil {
		return report, err
	}
	for _, result := range results {
		report.Uploaded += result.uploaded
		report.Skipped += result.skipped
	}
	return report, nil
}

func buildRestoreFilePlan(zr *zip.Reader, mapping idMapping, prepared *preparedNoteMediaRestore) (restoreFilePlan, error) {
	plan := restoreFilePlan{work: make([]restoreFileWork, 0)}
	for _, entry := range zr.File {
		if !strings.HasPrefix(entry.Name, "files/") {
			continue
		}
		item, included, err := planRestoreFile(entry, mapping, prepared)
		if err != nil {
			return restoreFilePlan{}, err
		}
		if !included {
			plan.skipped++
			continue
		}
		plan.work = append(plan.work, item)
	}
	return plan, nil
}

func planRestoreFile(entry *zip.File, mapping idMapping, prepared *preparedNoteMediaRestore) (restoreFileWork, bool, error) {
	if strings.Contains(entry.Name, "..") {
		return restoreFileWork{}, false, fmt.Errorf("backup: rejected path traversal entry %q", entry.Name)
	}
	oldKey := strings.TrimPrefix(entry.Name, "files/")
	if !hasAllowedPrefix(oldKey) {
		return restoreFileWork{}, false, fmt.Errorf("backup: rejected entry %q (not under %v)", entry.Name, bucketPrefixes)
	}
	key, included := remapRestoreFileKey(oldKey, mapping)
	if !included {
		return restoreFileWork{}, false, nil
	}
	item := restoreFileWork{entry: entry, key: key, isNote: strings.HasPrefix(oldKey, "notes/")}
	if item.isNote && prepared != nil {
		item.preparedFile, item.hasPrepared = prepared.files[oldKey]
	}
	return item, true, nil
}

func remapRestoreFileKey(oldKey string, mapping idMapping) (string, bool) {
	if _, _, _, isLinkKey := linkObjectID(oldKey); isLinkKey {
		return mapping.remapFileKey(oldKey)
	}
	if strings.HasPrefix(oldKey, "notes/") {
		return mapping.remapNoteFileKey(oldKey)
	}
	return oldKey, true
}

func (s *Service) wipeRestoreFiles(ctx context.Context, uid authctx.UserID, mode ConflictMode, ownedKeys []string, report *FileReport) error {
	if mode != ModeWipe || len(ownedKeys) == 0 {
		return nil
	}
	if err := s.storage.DeleteObjects(ctx, ownedKeys); err != nil {
		return fmt.Errorf("backup: delete owned objects: %w", err)
	}
	report.Wiped = int64(len(ownedKeys))
	return deleteWipedNoteMediaOwnership(ctx, s.pool, uid, ownedKeys)
}

func (s *Service) existingRestoreFiles(ctx context.Context, mode ConflictMode, work []restoreFileWork) (map[string]bool, error) {
	if mode != ModeSkip || len(work) == 0 {
		return map[string]bool{}, nil
	}
	keys := make([]string, len(work))
	for i := range work {
		keys[i] = work[i].key
	}
	existing, err := s.storage.ExistingObjects(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("backup: list existing restore objects: %w", err)
	}
	return existing, nil
}

func (s *Service) buildRestoreObjectTasks(work []restoreFileWork, existing map[string]bool, prepared *preparedNoteMediaRestore) []restoreObjectTask {
	tasks := make([]restoreObjectTask, len(work))
	for i := range work {
		item := work[i]
		tasks[i] = func(ctx context.Context) (restoreObjectResult, error) {
			return s.applyRestoreObject(ctx, item, existing[item.key], prepared)
		}
	}
	return tasks
}

func (s *Service) applyRestoreObject(ctx context.Context, item restoreFileWork, exists bool, prepared *preparedNoteMediaRestore) (restoreObjectResult, error) {
	if exists {
		return restoreObjectResult{skipped: 1}, nil
	}
	if item.isNote {
		return s.applyRestoreNoteObject(ctx, item, prepared)
	}
	return s.applyRestoreArchiveObject(ctx, item)
}

func (s *Service) applyRestoreNoteObject(ctx context.Context, item restoreFileWork, prepared *preparedNoteMediaRestore) (restoreObjectResult, error) {
	if item.hasPrepared {
		reader := io.NewSectionReader(prepared.spool, item.preparedFile.offset, item.preparedFile.size)
		if err := s.storage.PutObjectStream(ctx, item.key, reader, item.preparedFile.size, item.preparedFile.contentType); err != nil {
			return restoreObjectResult{}, fmt.Errorf("backup: put %q: %w", item.key, err)
		}
		return restoreObjectResult{uploaded: 1}, nil
	}
	optimized, err := optimizeRestoredNoteMedia(ctx, item.entry)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return restoreObjectResult{}, ctxErr
		}
		return restoreObjectResult{}, httperr.New(http.StatusBadRequest, "invalid_backup", "backup contains invalid note media")
	}
	if err := s.storage.PutObjectStream(ctx, item.key, bytes.NewReader(optimized.Data), int64(len(optimized.Data)), optimized.ContentType); err != nil {
		return restoreObjectResult{}, fmt.Errorf("backup: put %q: %w", item.key, err)
	}
	return restoreObjectResult{uploaded: 1}, nil
}

func (s *Service) applyRestoreArchiveObject(ctx context.Context, item restoreFileWork) (restoreObjectResult, error) {
	file, err := item.entry.Open()
	if err != nil {
		return restoreObjectResult{}, fmt.Errorf("backup: open zip entry %q: %w", item.entry.Name, err)
	}
	putErr := s.storage.PutObjectStream(ctx, item.key, file, int64(item.entry.UncompressedSize64), contentTypeFor(item.key))
	closeErr := file.Close()
	if putErr != nil {
		return restoreObjectResult{}, fmt.Errorf("backup: put %q: %w", item.key, putErr)
	}
	if closeErr != nil {
		return restoreObjectResult{}, fmt.Errorf("backup: close zip entry %q: %w", item.entry.Name, closeErr)
	}
	return restoreObjectResult{uploaded: 1}, nil
}

func deleteWipedNoteMediaOwnership(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, ownedKeys []string) error {
	noteKeys := make([]string, 0)
	for _, key := range ownedKeys {
		if strings.HasPrefix(key, "notes/") {
			noteKeys = append(noteKeys, key)
		}
	}
	if len(noteKeys) == 0 {
		return nil
	}
	_, err := pool.Exec(ctx, `
		DELETE FROM note_media m
		WHERE m.user_id = $1 AND m.object_key = ANY($2::text[])
		  AND NOT EXISTS (
			SELECT 1 FROM note_media_ref r
			WHERE r.user_id = m.user_id AND r.object_key = m.object_key
		  )
	`, int64(uid), noteKeys)
	if err != nil {
		return fmt.Errorf("backup: delete wiped note media ownership: %w", err)
	}
	return nil
}

const restoreObjectConcurrency = 8

type restoreObjectResult struct {
	uploaded int64
	skipped  int64
}

type restoreObjectTask func(context.Context) (restoreObjectResult, error)

func runRestoreObjectTasks(ctx context.Context, tasks []restoreObjectTask) ([]restoreObjectResult, error) {
	results := make([]restoreObjectResult, len(tasks))
	if len(tasks) == 0 {
		return results, nil
	}
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var workers sync.WaitGroup
	var errOnce sync.Once
	var firstErr error
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}
	startRestoreObjectWorkers(taskCtx, tasks, jobs, results, restoreWorkerCount(len(tasks)), &workers, fail)
	queueRestoreObjectTasks(taskCtx, jobs, len(tasks))
	workers.Wait()
	if firstErr != nil {
		return results, firstErr
	}
	return results, ctx.Err()
}

func restoreWorkerCount(taskCount int) int {
	if taskCount < restoreObjectConcurrency {
		return taskCount
	}
	return restoreObjectConcurrency
}

func startRestoreObjectWorkers(ctx context.Context, tasks []restoreObjectTask, jobs <-chan int, results []restoreObjectResult, count int, workers *sync.WaitGroup, fail func(error)) {
	workers.Add(count)
	for range count {
		go restoreObjectWorker(ctx, tasks, jobs, results, workers, fail)
	}
}

func restoreObjectWorker(ctx context.Context, tasks []restoreObjectTask, jobs <-chan int, results []restoreObjectResult, workers *sync.WaitGroup, fail func(error)) {
	defer workers.Done()
	for index := range jobs {
		if ctx.Err() != nil {
			return
		}
		result, err := tasks[index](ctx)
		if err != nil {
			fail(err)
			return
		}
		results[index] = result
	}
}

func queueRestoreObjectTasks(ctx context.Context, jobs chan<- int, count int) {
	defer close(jobs)
	for index := range count {
		select {
		case jobs <- index:
		case <-ctx.Done():
			return
		}
	}
}

func hasAllowedPrefix(key string) bool {
	for _, prefix := range bucketPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func contentTypeFor(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

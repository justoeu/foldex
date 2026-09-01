package backupagent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/backup"
	"foldex/internal/pkg/authctx"
)

// userZipKeyPrefix is the object-key namespace for per-user product ZIPs.
// One directory per user id; the file name is a fixed-width UTC timestamp,
// so lexicographic order within a user's prefix IS chronological order —
// retention needs no date parsing and no extra state.
const userZipKeyPrefix = "backups/users/"

// UserZipJob ships the per-user product backup (ADR-20's ZIP, produced by
// backup.Service.Export) for every ACTIVE account: spool → age → sha256 →
// external S3 under backups/users/<uid>/. The Export already guarantees
// INV-105 — the archive carries content only, never auth material — which is
// exactly why this artifact is worth shipping alongside the full dump: it is
// the one the USER can restore through /api/backup/restore without an
// operator.
//
// This job runs from its own process, so the backend's per-process HTTP
// archive slot (maxConcurrentArchiveOperations) never sees it — but each
// Export still competes with the live instance for the same Postgres and the
// same source-RustFS I/O. The night-time anchor is the only politeness
// available, which is why BACKUP_USERZIP_AT is a wall-clock anchor and not an
// interval.
type UserZipJob struct {
	cfg        Config
	store      Uploader
	recipients []age.Recipient
	logger     *slog.Logger

	// export is backup.Service.Export behind a seam: the real thing needs
	// Postgres plus the source bucket, and the pipeline around it (spool,
	// encrypt, hash, upload, retention, restore deference) is provable
	// without either. Production always wires the real Service.
	export      func(ctx context.Context, uid authctx.UserID, w io.Writer) error
	listActive  func(ctx context.Context) ([]authctx.UserID, error)
	restoreBusy func(ctx context.Context) (bool, error)
	now         func() time.Time
}

// NewUserZipJob builds the job over the real backup.Service (source bucket)
// and the external destination store.
func NewUserZipJob(cfg Config, pool *pgxpool.Pool, svc *backup.Service, store Uploader, logger *slog.Logger) (*UserZipJob, error) {
	recipients, err := parseRecipients(cfg.AgeRecipients)
	if err != nil {
		return nil, err
	}
	return &UserZipJob{
		cfg: cfg, store: store, recipients: recipients,
		logger: logger.With("job", JobUserZip),
		export: func(ctx context.Context, uid authctx.UserID, w io.Writer) error {
			_, err := svc.Export(ctx, uid, w, nil)
			return err
		},
		listActive:  listActiveUsers(pool),
		restoreBusy: func(ctx context.Context) (bool, error) { return restoreInFlight(ctx, pool) },
		now:         time.Now,
	}, nil
}

func listActiveUsers(pool *pgxpool.Pool) func(ctx context.Context) ([]authctx.UserID, error) {
	return func(ctx context.Context) ([]authctx.UserID, error) {
		rows, err := pool.Query(ctx, `SELECT id FROM app_user WHERE status = 'active' ORDER BY id`)
		if err != nil {
			return nil, fmt.Errorf("list active users: %w", err)
		}
		defer rows.Close()
		var uids []authctx.UserID
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scan user id: %w", err)
			}
			uids = append(uids, authctx.UserID(id))
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list active users: %w", err)
		}
		return uids, nil
	}
}

// Run executes one user_zip occurrence: ONE backup_run row for the whole job.
// A single user failing must not cost every other user their backup, so
// failures are collected per user and the run itself fails only when the
// listing fails or when every attempted export failed — anything less is a
// success whose meta names the stragglers for the admin surface to render.
func (j *UserZipJob) Run(ctx context.Context) (*Artifact, map[string]any, string, error) {
	uids, err := j.listActive(ctx)
	if err != nil {
		return nil, nil, ReasonUserZipFailed, err
	}

	var (
		failed     []int64
		deferred   []int64
		shipped    int
		bytesTotal int64
		pruneErr   bool
	)
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return nil, nil, ReasonUserZipFailed, err
		}
		// The Export reads the source bucket while a per-user restore leaves
		// it mid-write (the database is transactional, the bucket is not —
		// INV-104). Busy ⇒ this user skips THIS cycle and is counted, never
		// failed: the next anchor retries.
		busy, err := j.restoreBusy(ctx)
		if err != nil {
			failed = append(failed, int64(uid))
			j.logger.Error("restore probe failed", "user_id", int64(uid), "err", err)
			continue
		}
		if busy {
			deferred = append(deferred, int64(uid))
			j.logger.Info("user zip deferred: a restore is in flight", "user_id", int64(uid))
			continue
		}

		key, size, sha, err := j.shipOne(ctx, uid)
		if err != nil {
			failed = append(failed, int64(uid))
			j.logger.Error("user zip failed", "user_id", int64(uid), "err", err)
			continue
		}
		shipped++
		bytesTotal += size
		j.logger.Info("user zip shipped", "user_id", int64(uid), "key", key, "bytes", size, "sha256", sha)

		if j.cfg.RetentionMode == "agent" {
			if pruned, err := j.pruneUser(ctx, uid); err != nil {
				// The archive landed; a failed prune degrades to run metadata
				// exactly like the dump job's — never a failed backup.
				pruneErr = true
				j.logger.Warn("user zip retention prune failed", "user_id", int64(uid), "err", err)
			} else if pruned > 0 {
				j.logger.Info("user zip retention pruned", "user_id", int64(uid), "count", pruned)
			}
		}
	}

	if len(failed) > 0 && shipped == 0 {
		return nil, nil, ReasonUserZipFailed,
			fmt.Errorf("all %d attempted user exports failed", len(failed))
	}

	meta := map[string]any{
		"users":       len(uids),
		"shipped":     shipped,
		"bytes_total": bytesTotal,
	}
	if len(failed) > 0 {
		meta["failed_users"] = failed
	}
	if len(deferred) > 0 {
		meta["deferred_users"] = deferred
	}
	if pruneErr {
		meta["prune_error"] = ReasonPruneFailed
	}
	return nil, meta, "", nil
}

// shipOne exports one user's ZIP through the same spool→encrypt→hash→upload
// pipeline as the dump. The sha256 is of the CIPHERTEXT — what sha256sum sees
// in the bucket — and the spool exists because PutObjectStream needs a size
// up front, and an upload that dies mid-stream must never leave a truncated
// object that looks like a backup.
func (j *UserZipJob) shipOne(ctx context.Context, uid authctx.UserID) (string, int64, string, error) {
	started := j.now()

	// CreateTemp is born 0600; SpoolDir="" is the OS temp dir.
	spool, err := os.CreateTemp(j.cfg.SpoolDir, "foldex-userzip-*.spool")
	if err != nil {
		return "", 0, "", fmt.Errorf("create spool: %w", err)
	}
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()

	hasher := sha256.New()
	buffered := bufio.NewWriterSize(io.MultiWriter(spool, hasher), 1<<20)
	encrypter, err := encryptTo(buffered, j.recipients)
	if err != nil {
		return "", 0, "", err
	}
	if err := j.export(ctx, uid, encrypter); err != nil {
		return "", 0, "", fmt.Errorf("export: %w", err)
	}
	if err := encrypter.Close(); err != nil {
		return "", 0, "", fmt.Errorf("finish age stream: %w", err)
	}
	if err := buffered.Flush(); err != nil {
		return "", 0, "", fmt.Errorf("flush spool: %w", err)
	}

	size, err := spool.Seek(0, io.SeekEnd)
	if err != nil {
		return "", 0, "", fmt.Errorf("size spool: %w", err)
	}
	if size == 0 {
		return "", 0, "", fmt.Errorf("export produced no bytes")
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return "", 0, "", fmt.Errorf("rewind spool: %w", err)
	}

	key := userZipKey(uid, started, len(j.recipients) > 0)
	if err := j.store.PutObjectStream(ctx, key, spool, size, "application/octet-stream"); err != nil {
		return "", 0, "", fmt.Errorf("upload %s: %w", key, err)
	}
	return key, size, hex.EncodeToString(hasher.Sum(nil)), nil
}

// pruneUser keeps the newest RetainUserZip archives under ONE user's prefix
// and deletes the rest — never touching another user's directory, and never
// touching a key this job did not write (a foreign object in the namespace is
// a surprise, not a deletion target). RetainUserZip < 1 disables pruning: a
// misconfigured zero must not read as "delete every backup of every user".
func (j *UserZipJob) pruneUser(ctx context.Context, uid authctx.UserID) (int, error) {
	if j.cfg.RetainUserZip < 1 {
		return 0, nil
	}
	prefix := userZipPrefix(uid)
	var keys []string
	if err := j.store.WalkObjects(ctx, prefix, func(o ObjectInfo) error {
		if isUserZipKey(prefix, o.Key) {
			keys = append(keys, o.Key)
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("list user archives: %w", err)
	}
	if len(keys) <= j.cfg.RetainUserZip {
		return 0, nil
	}
	sort.Strings(keys)
	victims := keys[:len(keys)-j.cfg.RetainUserZip]
	if err := j.store.DeleteObjects(ctx, victims); err != nil {
		return 0, fmt.Errorf("delete %d pruned archives: %w", len(victims), err)
	}
	return len(victims), nil
}

func userZipPrefix(uid authctx.UserID) string {
	return fmt.Sprintf("%s%d/", userZipKeyPrefix, uid)
}

// userZipKey builds the object key for uid's archive taken at ts. Like the
// dump, the extension says what the bytes are: .zip.age needs `age -d` first.
func userZipKey(uid authctx.UserID, ts time.Time, encrypted bool) string {
	name := ts.UTC().Format("20060102-150405") + ".zip"
	if encrypted {
		name += ".age"
	}
	return userZipPrefix(uid) + name
}

// isUserZipKey reports whether key sits directly under prefix and looks like
// an archive this job wrote (timestamp name, .zip or .zip.age extension).
func isUserZipKey(prefix, key string) bool {
	rest, found := strings.CutPrefix(key, prefix)
	if !found || strings.Contains(rest, "/") {
		return false
	}
	if trimmed, ok := strings.CutSuffix(rest, ".age"); ok {
		rest = trimmed
	}
	stamp, ok := strings.CutSuffix(rest, ".zip")
	if !ok {
		return false
	}
	_, err := time.Parse("20060102-150405", stamp)
	return err == nil
}

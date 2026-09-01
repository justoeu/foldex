package backupagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/resourcebudget"
)

// mirrorKeyPrefix is where source objects land in the external bucket: the
// destination key is always this prefix + the source key, so a DR restore is
// a plain prefix copy back.
const mirrorKeyPrefix = "backups/rustfs/"

// mirrorWatermarkOverlap is subtracted from the last success's started_at
// when building the watermark. It absorbs clock skew between the agent and
// the object store AND objects written while the previous run was already
// listing — without it, anything uploaded during a long run's window is
// silently never mirrored.
const mirrorWatermarkOverlap = time.Hour

const (
	defaultRestoreProbeEvery    = time.Minute
	defaultRestoreProbeDeadline = 10 * time.Minute
)

// MirrorJob incrementally copies the source bucket (RustFS) into the external
// S3 target. Deletions deliberately never propagate: a compromised backend
// that wipes objects must not be able to wipe their backup copy too —
// propagating deletes converts ransomware into loss of the backup
// (SDD-OPS-BACKUP §5.3).
// SourceBucket is the READ-ONLY slice of the origin the mirror consumes —
// deliberately narrower than Uploader: the origin must never see a Put or a
// Delete from this job, and the type system is cheaper than discipline.
type SourceBucket interface {
	WalkObjects(ctx context.Context, prefix string, visit func(ObjectInfo) error) error
	OpenObject(ctx context.Context, key string) (io.ReadCloser, error)
}

type MirrorJob struct {
	source SourceBucket
	dest   Uploader
	logger *slog.Logger

	// recipients mirror the dump's encryption policy: object bytes are the
	// ONE payload the encrypted dump does not carry, so the mirror is the
	// only channel taking user media off the machine — shipping it plaintext
	// would undo what INV-171 promises about the external bucket. Objects
	// land as <key>.age; `age -d` per object is the DR story.
	recipients []age.Recipient
	spoolDir   string

	// lastSuccess and restoreBusy are seams over RunStore/lock probing so the
	// diff-and-copy pipeline is provable without a database.
	lastSuccess func(context.Context) (time.Time, error)
	restoreBusy func(context.Context) (bool, error)

	// The wait-for-restore loop is contained in the job on purpose: the
	// scheduler stays generic, and a per-user restore that outlives the
	// deadline becomes a normal failed(restore_in_flight) row the next slot
	// retries. Injectable so tests do not sleep for real minutes.
	restoreProbeEvery    time.Duration
	restoreProbeDeadline time.Duration
}

func NewMirrorJob(cfg Config, pool *pgxpool.Pool, runs *RunStore, source SourceBucket, dest Uploader, logger *slog.Logger) (*MirrorJob, error) {
	recipients, err := parseRecipients(cfg.AgeRecipients)
	if err != nil {
		return nil, err
	}
	return &MirrorJob{
		source:     source,
		dest:       dest,
		recipients: recipients,
		spoolDir:   cfg.SpoolDir,
		logger:     logger.With("job", JobMirror),
		lastSuccess: func(ctx context.Context) (time.Time, error) {
			return runs.LastSuccess(ctx, JobMirror)
		},
		restoreBusy: func(ctx context.Context) (bool, error) {
			return restoreInFlight(ctx, pool)
		},
		restoreProbeEvery:    defaultRestoreProbeEvery,
		restoreProbeDeadline: defaultRestoreProbeDeadline,
	}, nil
}

// encrypted reports whether destination objects carry the .age envelope.
func (j *MirrorJob) encrypted() bool { return len(j.recipients) > 0 }

// Run executes one mirror pass: wait out any per-user restore, list both
// sides, copy the delta. Signature matches jobSpec.run.
func (j *MirrorJob) Run(ctx context.Context) (*Artifact, map[string]any, string, error) {
	if reason, err := j.waitRestoreClear(ctx); err != nil {
		return nil, nil, reason, err
	}

	last, err := j.lastSuccess(ctx)
	if err != nil {
		return nil, nil, ReasonMirrorScanFailed, fmt.Errorf("read watermark: %w", err)
	}
	watermark := mirrorWatermark(last)

	var src []ObjectInfo
	var suspicious int
	if err := j.source.WalkObjects(ctx, "", func(o ObjectInfo) error {
		// Defense in depth behind linkObjectID's grammar: a key carrying a
		// ".." component could, on a destination that normalizes paths,
		// escape backups/rustfs/ and overwrite the encrypted dumps. Such a
		// key is never legitimate — skip it loudly, never fail the whole
		// pass over it (a permanent poison object would end mirroring
		// forever).
		if hasTraversal(o.Key) {
			suspicious++
			j.logger.Warn("skipping source key with path traversal material", "key", o.Key)
			return nil
		}
		src = append(src, o)
		return nil
	}); err != nil {
		return nil, nil, ReasonMirrorScanFailed, fmt.Errorf("list source: %w", err)
	}
	dst := make(map[string]ObjectInfo)
	if err := j.dest.WalkObjects(ctx, mirrorKeyPrefix, func(o ObjectInfo) error {
		dst[strings.TrimPrefix(o.Key, mirrorKeyPrefix)] = o
		return nil
	}); err != nil {
		return nil, nil, ReasonMirrorScanFailed, fmt.Errorf("list destination: %w", err)
	}

	delta := mirrorDelta(src, dst, watermark, j.encrypted())
	bytesCopied, err := j.copyDelta(ctx, delta)
	if err != nil {
		return nil, nil, ReasonMirrorCopyFailed, err
	}

	stats := &MirrorStats{
		ObjectsScanned: int64(len(src)),
		ObjectsCopied:  int64(len(delta)),
		BytesCopied:    bytesCopied,
	}
	meta := map[string]any{
		"objects_skipped": int64(len(src) - len(delta)),
		"encrypted":       j.encrypted(),
	}
	if suspicious > 0 {
		meta["suspicious_keys_skipped"] = suspicious
	}
	j.logger.Info("mirror pass complete",
		"scanned", stats.ObjectsScanned, "copied", stats.ObjectsCopied, "bytes", stats.BytesCopied)
	return &Artifact{Mirror: stats}, meta, "", nil
}

// waitRestoreClear defers the pass while a per-user restore holds
// RestoreAdvisoryLockKey: the database side of a restore is transactional but
// the bucket is not (INV-104), so mirroring mid-restore would ship a
// half-written object set.
func (j *MirrorJob) waitRestoreClear(ctx context.Context) (string, error) {
	deadline := time.Now().Add(j.restoreProbeDeadline)
	for {
		busy, err := j.restoreBusy(ctx)
		if err != nil {
			return ReasonMirrorScanFailed, fmt.Errorf("probe restore lock: %w", err)
		}
		if !busy {
			return "", nil
		}
		if !time.Now().Before(deadline) {
			return ReasonRestoreInFlight, errors.New("a per-user restore still holds the bucket after the wait deadline")
		}
		j.logger.Info("per-user restore in flight; waiting before mirroring")
		select {
		case <-ctx.Done():
			return ReasonRestoreInFlight, ctx.Err()
		case <-time.After(j.restoreProbeEvery):
		}
	}
}

// mirrorWatermark turns the last success into the diff threshold. The zero
// time (never succeeded) stays zero: every object is "modified since", which
// is exactly what a first full copy means.
func mirrorWatermark(lastSuccess time.Time) time.Time {
	if lastSuccess.IsZero() {
		return time.Time{}
	}
	return lastSuccess.Add(-mirrorWatermarkOverlap)
}

// mirrorDelta decides which source objects need copying: absent at the
// destination, size mismatch, or modified at/after the watermark. ETag is
// deliberately NEVER consulted: a multipart etag is md5(concat(md5s))-N — a
// function of each upload's part size, not of the content — so source and
// destination etags never match and an etag diff would re-copy the whole
// bucket every run, forever, silently (SDD-OPS-BACKUP §11.6).
func mirrorDelta(src []ObjectInfo, dst map[string]ObjectInfo, watermark time.Time, encrypted bool) []ObjectInfo {
	var out []ObjectInfo
	for _, s := range src {
		d, present := dst[destSuffixKey(s.Key, encrypted)]
		// Size is a diff criterion ONLY for plaintext copies: an encrypted
		// destination holds the age CIPHERTEXT, whose length never equals the
		// source's — comparing them would re-copy the whole bucket forever,
		// the same failure mode the ETag rule guards against.
		sizeDiffers := !encrypted && present && d.Size != s.Size
		if !present || sizeDiffers || !s.LastModified.Before(watermark) {
			out = append(out, s)
		}
	}
	return out
}

// destSuffixKey is the destination key RELATIVE to the mirror prefix.
func destSuffixKey(srcKey string, encrypted bool) string {
	if encrypted {
		return srcKey + ".age"
	}
	return srcKey
}

// hasTraversal reports whether any path component of key is "..".
func hasTraversal(key string) bool {
	for _, part := range strings.Split(key, "/") {
		if part == ".." {
			return true
		}
	}
	return strings.HasPrefix(key, "/")
}

// copyDelta streams the delta source→destination through a bounded worker
// pool with fail-fast: the first error cancels the rest — a run half-broken
// against an unreachable store must fail promptly, not grind through
// thousands of doomed uploads. The next slot's watermark overlap re-covers
// whatever this pass did not finish.
func (j *MirrorJob) copyDelta(ctx context.Context, delta []ObjectInfo) (int64, error) {
	if len(delta) == 0 {
		return 0, nil
	}
	workers := len(delta)
	if workers > resourcebudget.BackgroundWorkerConcurrency {
		workers = resourcebudget.BackgroundWorkerConcurrency
	}

	copyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan ObjectInfo)
	var wg sync.WaitGroup
	var errOnce sync.Once
	var firstErr error
	var bytesCopied atomic.Int64
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for o := range jobs {
				if copyCtx.Err() != nil {
					return
				}
				if err := j.copyOne(copyCtx, o); err != nil {
					fail(err)
					return
				}
				bytesCopied.Add(o.Size)
			}
		}()
	}
	for _, o := range delta {
		select {
		case jobs <- o:
		case <-copyCtx.Done():
		}
		if copyCtx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return bytesCopied.Load(), firstErr
	}
	return bytesCopied.Load(), ctx.Err()
}

func (j *MirrorJob) copyOne(ctx context.Context, o ObjectInfo) error {
	rc, err := j.source.OpenObject(ctx, o.Key)
	if err != nil {
		return fmt.Errorf("open source %q: %w", o.Key, err)
	}
	defer rc.Close()
	destKey := mirrorKeyPrefix + destSuffixKey(o.Key, j.encrypted())
	if !j.encrypted() {
		if err := j.dest.PutObjectStream(ctx, destKey, rc, o.Size, "application/octet-stream"); err != nil {
			return fmt.Errorf("copy %q: %w", o.Key, err)
		}
		return nil
	}
	// Encrypted path spools per object: PutObjectStream needs the ciphertext
	// size up front, and age's length differs from the source's. Objects here
	// are re-encoded images of a few MB — the spool is small and short-lived.
	spool, err := os.CreateTemp(j.spoolDir, "foldex-mirror-*.spool")
	if err != nil {
		return fmt.Errorf("create mirror spool: %w", err)
	}
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()
	enc, err := encryptTo(spool, j.recipients)
	if err != nil {
		return err
	}
	if _, err := io.Copy(enc, rc); err != nil {
		return fmt.Errorf("encrypt %q: %w", o.Key, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("finish age stream for %q: %w", o.Key, err)
	}
	size, err := spool.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("size mirror spool: %w", err)
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind mirror spool: %w", err)
	}
	if err := j.dest.PutObjectStream(ctx, destKey, spool, size, "application/octet-stream"); err != nil {
		return fmt.Errorf("copy %q: %w", o.Key, err)
	}
	return nil
}

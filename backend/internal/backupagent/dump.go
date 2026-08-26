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
	"os/exec"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Uploader is the slice of the storage client the dump job needs. Narrow on
// purpose, like backup.StorageBucket: tests exercise the pipeline against a
// recorder without standing up RustFS or S3.
type Uploader interface {
	PutObjectStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// OpenObject streams a stored artifact back — the drill's read path. It
	// downloads the REAL bytes from the bucket precisely because a drill of a
	// local copy would prove less than "the backup we shipped restores".
	OpenObject(ctx context.Context, key string) (io.ReadCloser, error)
	WalkObjects(ctx context.Context, prefix string, visit func(ObjectInfo) error) error
	DeleteObjects(ctx context.Context, keys []string) error
}

// ObjectInfo mirrors storage.ObjectInfo without importing the package into
// every test. ETag is carried but is NEVER a mirror diff criterion — multipart
// etags depend on the uploader's part size, not the content (SDD §11.6).
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

// DumpJob owns one full-database export: pg_dump -Fc → age → sha256 → spool →
// upload → GFS prune.
type DumpJob struct {
	cfg        Config
	pool       *pgxpool.Pool
	store      Uploader
	recipients []age.Recipient
	logger     *slog.Logger

	// command builds the dump process. Overridable in tests so the pipeline
	// (encrypt, hash, spool, upload, prune) is provable without a pg_dump
	// binary on the test host; production always uses pgDumpCommand.
	command func(ctx context.Context, cfg Config, snapshotID string) *exec.Cmd
	// snapshotCounts opens a REPEATABLE READ transaction, exports its
	// snapshot and reads the sanity counts INSIDE it; pg_dump then attaches
	// to the very same snapshot via --snapshot. Anything less and the counts
	// and the archive describe two different instants — on a live instance a
	// single click between them turns every weekly drill into a spurious
	// drill_counts_mismatch. The returned release holds the transaction open
	// until pg_dump has finished; nil seam (tests without a pool) means the
	// dump ships without counts and the drill compares schema version only.
	snapshotCounts func(ctx context.Context) (map[string]int64, int64, string, func(), error)
	now            func() time.Time
}

func NewDumpJob(cfg Config, pool *pgxpool.Pool, store Uploader, logger *slog.Logger) (*DumpJob, error) {
	recipients, err := parseRecipients(cfg.AgeRecipients)
	if err != nil {
		return nil, err
	}
	j := &DumpJob{
		cfg: cfg, pool: pool, store: store, recipients: recipients,
		logger:  logger.With("job", JobDump),
		command: pgDumpCommand,
		now:     time.Now,
	}
	if pool != nil {
		j.snapshotCounts = func(ctx context.Context) (map[string]int64, int64, string, func(), error) {
			return snapshotSanityCounts(ctx, pool)
		}
	}
	return j, nil
}

// pgDumpCommand builds the real dump invocation. No -C: baking CREATE DATABASE
// into the artifact couples it to the source cluster's locale/provider, and
// both the drill and a real disaster recovery create the target database
// themselves from template0 (SDD-OPS-BACKUP §5.1). The password travels via
// PGPASSWORD, never argv — argv is world-readable in /proc.
func pgDumpCommand(ctx context.Context, cfg Config, snapshotID string) *exec.Cmd {
	args := []string{
		"--format=custom",
		"--no-password",
		"--host=" + cfg.PGHost,
		fmt.Sprintf("--port=%d", cfg.PGPort),
		"--username=" + cfg.PGUser,
	}
	if snapshotID != "" {
		// The dump reads the exact snapshot the sanity counts were taken in
		// (see DumpJob.snapshotCounts) — the drill's equality verdict depends
		// on both sides describing one instant.
		args = append(args, "--snapshot="+snapshotID)
	}
	args = append(args, cfg.PGDatabase)
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	cmd.Env = append(os.Environ(),
		"PGPASSWORD="+cfg.PGPassword,
		"PGSSLMODE="+cfg.PGSSLMode,
	)
	return cmd
}

// Run executes one dump. The error is for the log; the normalized reason for
// backup_run is returned separately so raw tool output never reaches the
// table (its stderr can carry a DSN, and the column feeds the UI and alerts).
func (j *DumpJob) Run(ctx context.Context) (*Artifact, map[string]any, string, error) {
	started := j.now()

	// Source counts are read BEFORE pg_dump, from the same pool the dump
	// reads: they are the yardstick the drill compares the restored database
	// against. Failing to read them must not fail the backup itself — the
	// artifact is the product — so the dump degrades to shipping without them
	// and the drill falls back to schema-version-only comparison.
	var sourceTables map[string]int64
	var sourceSchema int64
	var snapshotID string
	haveCounts := false
	if j.snapshotCounts != nil {
		tables, schema, snap, release, err := j.snapshotCounts(ctx)
		if err != nil {
			j.logger.Warn("source table counts unavailable — the drill will compare schema version only", "err", err)
		} else {
			sourceTables, sourceSchema, snapshotID = tables, schema, snap
			haveCounts = true
			if release != nil {
				// The exporting transaction must outlive pg_dump: the
				// snapshot dies with it, and pg_dump refuses a dead one.
				defer release()
			}
		}
	}

	// CreateTemp is born 0600; SpoolDir="" is the OS temp dir (the container's
	// writable layer — see Config.SpoolDir for when to point it at a volume).
	spool, err := os.CreateTemp(j.cfg.SpoolDir, "foldex-dump-*.spool")
	if err != nil {
		return nil, nil, ReasonSpoolFailed, fmt.Errorf("create spool: %w", err)
	}
	defer func() {
		spool.Close()
		os.Remove(spool.Name())
	}()

	// The hash is taken over the CIPHERTEXT — what actually sits in the
	// bucket — so an operator can verify the artifact with sha256sum without
	// decrypting anything.
	hasher := sha256.New()
	buffered := bufio.NewWriterSize(io.MultiWriter(spool, hasher), 1<<20)

	cmd := j.command(ctx, j.cfg, snapshotID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, ReasonDumpFailed, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, nil, ReasonDumpFailed, fmt.Errorf("start pg_dump: %w", err)
	}

	encrypter, err := encryptTo(buffered, j.recipients)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, ReasonEncryptFailed, err
	}
	_, copyErr := io.Copy(encrypter, stdout)
	closeErr := encrypter.Close()
	waitErr := cmd.Wait()
	switch {
	case waitErr != nil:
		return nil, nil, ReasonDumpFailed, fmt.Errorf("pg_dump: %w (stderr: %s)", waitErr, firstLine(stderr.String()))
	case copyErr != nil:
		return nil, nil, ReasonSpoolFailed, fmt.Errorf("copy dump stream: %w", copyErr)
	case closeErr != nil:
		return nil, nil, ReasonEncryptFailed, fmt.Errorf("finish age stream: %w", closeErr)
	}
	if err := buffered.Flush(); err != nil {
		return nil, nil, ReasonSpoolFailed, fmt.Errorf("flush spool: %w", err)
	}

	size, err := spool.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, nil, ReasonSpoolFailed, fmt.Errorf("size spool: %w", err)
	}
	if size == 0 {
		// pg_dump exiting 0 with zero bytes is not a backup; refuse to record
		// success over an empty artifact.
		return nil, nil, ReasonDumpFailed, fmt.Errorf("pg_dump produced no output")
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, nil, ReasonSpoolFailed, fmt.Errorf("rewind spool: %w", err)
	}

	key := dumpKey(started, len(j.recipients) > 0)
	if err := j.store.PutObjectStream(ctx, key, spool, size, "application/octet-stream"); err != nil {
		return nil, nil, ReasonUploadFailed, fmt.Errorf("upload %s: %w", key, err)
	}

	artifact := &Artifact{Key: key, Bytes: size, SHA256: hex.EncodeToString(hasher.Sum(nil))}
	meta := map[string]any{
		"encrypted": len(j.recipients) > 0,
	}
	if haveCounts {
		meta["tables"] = sourceTables
		meta["schema_version"] = sourceSchema
	}

	if j.cfg.RetentionMode == "agent" {
		if pruned, err := j.prune(ctx); err != nil {
			// The dump itself landed; a failed prune is a warning on this run,
			// not a failed backup. It gets its own visibility instead of
			// poisoning the success the operator actually cares about.
			j.logger.Warn("retention prune failed", "err", err)
			meta["prune_error"] = ReasonPruneFailed
		} else if pruned > 0 {
			meta["pruned_objects"] = pruned
		}
	}
	return artifact, meta, "", nil
}

// prune applies the GFS policy over the dump namespace.
func (j *DumpJob) prune(ctx context.Context) (int, error) {
	var keys []string
	if err := j.store.WalkObjects(ctx, dumpKeyPrefix, func(o ObjectInfo) error {
		keys = append(keys, o.Key)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("list dumps: %w", err)
	}
	policy := GFSPolicy{Daily: j.cfg.RetainDaily, Weekly: j.cfg.RetainWeekly, Monthly: j.cfg.RetainMonthly}
	victims := policy.prunable(keys)
	if len(victims) == 0 {
		return 0, nil
	}
	if err := j.store.DeleteObjects(ctx, victims); err != nil {
		return 0, fmt.Errorf("delete %d pruned dumps: %w", len(victims), err)
	}
	j.logger.Info("retention pruned dumps", "count", len(victims))
	return len(victims), nil
}

// VersionSkewWarning compares the server's major with pg_dump's. Real drift
// fails loudly anyway (pg_dump refuses a newer server); the warning exists for
// the review window where compose bumped and the agent image did not.
func (j *DumpJob) VersionSkewWarning(ctx context.Context) string {
	var server string
	if err := j.pool.QueryRow(ctx, `SHOW server_version`).Scan(&server); err != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, "pg_dump", "--version").Output()
	if err != nil {
		return ""
	}
	client := strings.TrimSpace(string(out))
	if major(server) != "" && major(client) != "" && major(server) != major(client) {
		return fmt.Sprintf("pg_dump major (%s) differs from server major (%s) — align backend/Dockerfile backup-agent stage with the compose pin", major(client), major(server))
	}
	return ""
}

func major(version string) string {
	for _, tok := range strings.Fields(version) {
		if tok != "" && tok[0] >= '0' && tok[0] <= '9' {
			maj, _, _ := strings.Cut(tok, ".")
			return maj
		}
	}
	return ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

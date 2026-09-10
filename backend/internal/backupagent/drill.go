package backupagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/backupjobs"
)

// sanityTables are the content tables whose row counts travel in the dump's
// meta ("tables") and are recounted in the restored database. A fixed,
// compiled list on BOTH sides: the names are interpolated into SQL, and a
// list that came from data would turn the drill into an injection surface.
var sanityTables = []string{"app_user", "folder", "tag", "link", "note", "link_tag", "click_log"}

// drillDatabase is the target created inside the ephemeral cluster. The name
// is irrelevant to the artifact — the dump has no -C, so pg_restore fills
// whatever database it is pointed at.
const drillDatabase = "foldex_drill"

// drillPort pins the ephemeral server's port explicitly on both sides of the
// unix socket. Relying on the compiled default would let a stray PGPORT in
// the environment point clients at a socket the server never opened.
const drillPort = "5432"

// drillRuns is the slice of RunStore the drill needs; a fake stands in for it
// in the unit tests, which have no database.
type drillRuns interface {
	LatestSucceededDump(ctx context.Context) (*backupjobs.DumpRunRef, error)
	SetDrillSource(ctx context.Context, runID, sourceRunID int64) error
}

// DrillJob proves the newest shipped dump actually restores: download the
// REAL artifact from the bucket, verify its recorded digest, decrypt it with
// the private age identity, restore it into a disposable Postgres cluster
// inside this container, and compare row counts against the source's meta.
// A backup that was never restored is hope, not a backup (SDD-OPS-BACKUP §5.2).
type DrillJob struct {
	cfg        Config
	runs       drillRuns
	store      Uploader
	identities []age.Identity
	logger     *slog.Logger

	// command is the exec seam (pgDumpCommand precedent): every external
	// binary the drill runs — initdb, pg_ctl, createdb, pg_restore — goes
	// through it, so the pipeline is provable on hosts without a Postgres
	// server. Production always uses execCommand.
	command func(ctx context.Context, name string, args ...string) *exec.Cmd
	// readCounts queries the RESTORED cluster over its unix socket. A seam
	// because the stubbed cluster of the unit tests cannot answer SQL;
	// production always uses queryRestoredCounts.
	readCounts func(ctx context.Context, socketDir, database string) (map[string]int64, int64, error)
}

func NewDrillJob(cfg Config, runs drillRuns, store Uploader, logger *slog.Logger) (*DrillJob, error) {
	j := &DrillJob{
		cfg: cfg, runs: runs, store: store,
		logger:  logger.With("job", backupjobs.JobDrill),
		command: execCommand,
	}
	j.readCounts = func(ctx context.Context, socketDir, database string) (map[string]int64, int64, error) {
		return queryRestoredCounts(ctx, socketDir, database, cfg.PGUser)
	}
	// The identity loads at construction, not first run: a bad or missing
	// file is a configuration error and must fail the boot (keyfile posture —
	// no autogenerate, no ephemeral fallback), not surface weeks later as the
	// first failed drill.
	identities, err := loadIdentities(cfg)
	if err != nil {
		return nil, err
	}
	j.identities = identities
	return j, nil
}

func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- production callers pass literal binaries (initdb/pg_ctl/createdb/pg_restore); the name is a test seam, never request input
	// Minimal environment, not os.Environ(): the drill EXECUTES bytes that
	// came from the bucket (pg_restore runs the archive's SQL), and a
	// malicious artifact restored as superuser can spawn children that read
	// the environment — handing those children BACKUP_S3_* or the identity
	// path would undo the isolation INV-171 exists for.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=C",
		"TZ=" + os.Getenv("TZ"),
	}
	return cmd
}

// loadIdentities resolves the configured identity, whichever way it arrived.
// nil, nil when none is configured: that is a capability gap the agent
// reports (no_identity), not a boot error — plaintext deployments have no
// identity to load.
func loadIdentities(cfg Config) ([]age.Identity, error) {
	switch {
	case cfg.AgeIdentity != "":
		return parseInlineIdentity(cfg.AgeIdentity)
	case strings.TrimSpace(cfg.AgeIdentityFile) != "":
		return loadAgeIdentities(cfg.AgeIdentityFile)
	}
	return nil, nil
}

// parseInlineIdentity parses BACKUP_AGE_IDENTITY. The message never echoes
// the value: the likely paste mistake here IS a private key, and this line
// flows to container logs.
func parseInlineIdentity(raw string) ([]age.Identity, error) {
	identities, err := age.ParseIdentities(strings.NewReader(raw))
	if err != nil {
		return nil, errors.New("backupagent: BACKUP_AGE_IDENTITY holds no parseable age identities")
	}
	return identities, nil
}

// loadAgeIdentities reads the private age identity file for the drill. Error
// messages never echo file content (encrypt.go precedent: the one value that
// could land here is a private key, and this flows to container logs).
func loadAgeIdentities(path string) ([]age.Identity, error) {
	// Operator configuration, never request input — same gosec posture as
	// keyfile.Read.
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		// The identity decrypts EVERY backup; the VAPID key's 0600 rule
		// (INV-117) applies with more force here. Group/world bits are a
		// configuration error worth refusing at boot.
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("backupagent: BACKUP_AGE_IDENTITY_FILE (%s) is mode %o — must not be group- or world-readable (chmod 600)", path, info.Mode().Perm())
		}
	}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-configured BACKUP_AGE_IDENTITY_FILE, never request input
	if err != nil {
		return nil, fmt.Errorf("backupagent: BACKUP_AGE_IDENTITY_FILE: %w", err)
	}
	identities, err := age.ParseIdentities(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("backupagent: BACKUP_AGE_IDENTITY_FILE (%s) holds no parseable age identities", path)
	}
	return identities, nil
}

// Run executes one drill under the backup_run row runID. It never returns an
// Artifact — a drill ships nothing; its product is the verdict.
func (j *DrillJob) Run(ctx context.Context, runID int64) (*backupjobs.Artifact, map[string]any, string, error) {
	src, err := j.runs.LatestSucceededDump(ctx)
	if errors.Is(err, backupjobs.ErrNoDumpToDrill) {
		return nil, nil, backupjobs.ReasonDrillNoDump, err
	}
	if err != nil {
		// A database that cannot answer is NOT "nothing to validate": the
		// no-dump token would tell the operator the opposite of the truth.
		return nil, nil, backupjobs.ReasonDrillSourceFailed, err
	}
	if err := j.runs.SetDrillSource(ctx, runID, src.ID); err != nil {
		// The meta below still records the source; losing the column linkage
		// is not worth abandoning the validation itself.
		j.logger.Warn("could not stamp drill_of_run_id", "err", err)
	}
	j.logger.Info("drilling dump", "source_run_id", src.ID, "artifact_key", src.Key)

	// One disposable directory for everything — spool, decrypted dump, data
	// dir, unix socket — so a single RemoveAll is the whole cleanup story.
	// Stale siblings first: a SIGKILL/OOM mid-drill leaves a multi-GB
	// foldex-drill-* orphan the defer never saw, and the next run is the
	// only janitor this directory has.
	sweepOrphanDrillDirs(j.cfg.SpoolDir, j.logger)
	dir, err := os.MkdirTemp(j.cfg.SpoolDir, "foldex-drill-*")
	if err != nil {
		return nil, nil, backupjobs.ReasonSpoolFailed, fmt.Errorf("create drill dir: %w", err)
	}
	clusterInitialized := false
	dataDir := filepath.Join(dir, "data")
	defer func() {
		// Unconditional teardown, on a fresh context: the run's ctx is
		// exactly what shutdown cancels, and an orphaned postmaster would
		// outlive the job holding the data dir open. The stop is attempted
		// whenever initdb created a data dir — NOT only after a successful
		// `pg_ctl start`: a start that exits nonzero on its --timeout can
		// leave a postmaster still coming up, and gating on success is
		// exactly the path that would RemoveAll under a live server.
		if clusterInitialized {
			stopCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
			if err := j.command(stopCtx, "pg_ctl", "stop", "--pgdata="+dataDir, "--mode=immediate", "--wait").Run(); err != nil {
				// A stop that fails on a cluster that never started is the
				// expected noise; one that fails otherwise deserves a line —
				// the RemoveAll below may be about to race a live postmaster.
				j.logger.Warn("drill cluster stop reported an error", "err", err)
			}
			done()
		}
		if err := os.RemoveAll(dir); err != nil {
			j.logger.Warn("drill teardown left files behind", "dir", dir, "err", err)
		}
	}()

	// Download the REAL bytes from the bucket, hashing as they land: this
	// validates the stored object and the encryption round-trip in one pass —
	// a drill of a local copy would prove less.
	spoolPath := filepath.Join(dir, "artifact.spool")
	digest, err := j.download(ctx, src.Key, spoolPath)
	if err != nil {
		return nil, nil, backupjobs.ReasonDrillDownloadFailed, err
	}
	if digest != src.SHA256 {
		return nil, nil, backupjobs.ReasonDrillDigestMismatch,
			fmt.Errorf("artifact %s digest %s does not match recorded %s — the bytes in the bucket are not the bytes the dump shipped", src.Key, digest, src.SHA256)
	}

	// With identities configured, a plaintext artifact key is refused rather
	// than restored: artifact_key comes from a database column, and accepting
	// an un-suffixed key would let anyone with UPDATE on backup_run strip the
	// authentication that age gives every legitimate dump for free.
	if len(j.identities) > 0 && !j.cfg.AllowPlaintext && !strings.HasSuffix(src.Key, ".age") {
		return nil, nil, backupjobs.ReasonDrillDecryptFailed,
			fmt.Errorf("artifact %s is not .age but this instance encrypts its dumps — refusing to restore unauthenticated bytes", src.Key)
	}
	dumpPath, err := j.decrypt(src, spoolPath, filepath.Join(dir, "restore.dump"))
	if err != nil {
		return nil, nil, backupjobs.ReasonDrillDecryptFailed, err
	}

	// Ephemeral cluster: same superuser name as production (ownership in the
	// artifact restores without remapping), unix socket only, tuned for a
	// throwaway (fsync off is safe for data that dies with the run).
	if err := j.exec(ctx, "initdb",
		"--pgdata="+dataDir,
		"--username="+j.cfg.PGUser,
		"--locale=C",
		"--encoding=UTF8",
	); err != nil {
		return nil, nil, backupjobs.ReasonDrillRestoreFailed, err
	}
	clusterInitialized = true
	serverOpts := fmt.Sprintf("-c listen_addresses='' -c unix_socket_directories='%s' -c port=%s"+
		" -c fsync=off -c synchronous_commit=off -c shared_buffers=64MB -c max_connections=10 -c autovacuum=off",
		dir, drillPort)
	if err := j.exec(ctx, "pg_ctl", "start",
		"--pgdata="+dataDir,
		"--wait", "--timeout=60",
		"--log="+filepath.Join(dir, "postgres.log"),
		"-o", serverOpts,
	); err != nil {
		return nil, nil, backupjobs.ReasonDrillRestoreFailed, err
	}

	// template0: the artifact deliberately carries no CREATE DATABASE (dump
	// has no -C), decoupling it from the source cluster's locale/provider —
	// the drill creates the target the way a real disaster recovery would.
	if err := j.exec(ctx, "createdb",
		"--host="+dir, "--port="+drillPort,
		"--username="+j.cfg.PGUser,
		"--template=template0", "--encoding=UTF8",
		drillDatabase,
	); err != nil {
		return nil, nil, backupjobs.ReasonDrillRestoreFailed, err
	}
	// --no-owner --no-privileges: the ephemeral cluster is initdb'd with ONE
	// role (cfg.PGUser), so every `ALTER ... OWNER TO` and `GRANT` the source
	// cluster emitted names a role that deliberately does not exist here. With
	// --exit-on-error the first of them aborts the whole restore, and the
	// drill then reports drill_restore_failed for a difference it created
	// itself — the artifact is fine. Observed against a managed Postgres whose
	// dump references `postgres`: every drill failed, so the instance had a
	// green dump and a restore that had never once been proven.
	//
	// This does NOT weaken the drill. What it proves is that the SCHEMA and
	// the ROWS come back — the row counts are compared right below. Replaying
	// grants into a cluster whose role list is a fixture proves nothing about
	// the backup; a real recovery restores into a cluster that already has the
	// roles, and keeps ownership.
	if err := j.exec(ctx, "pg_restore",
		"--host="+dir, "--port="+drillPort,
		"--username="+j.cfg.PGUser,
		"--dbname="+drillDatabase,
		"--no-owner", "--no-privileges",
		"--jobs=1", "--exit-on-error",
		dumpPath,
	); err != nil {
		return nil, nil, backupjobs.ReasonDrillRestoreFailed, err
	}

	sourceMeta, err := parseDumpMeta(src.Meta)
	if err != nil {
		return nil, nil, backupjobs.ReasonDrillCountsMismatch, err
	}
	if len(sourceMeta.Tables) == 0 {
		j.logger.Warn("source dump carries no table counts (pre-PR2 artifact) — sanity degrades to schema version only")
	}
	gotTables, gotVersion, err := j.readCounts(ctx, dir, drillDatabase)
	if err != nil {
		return nil, nil, backupjobs.ReasonDrillRestoreFailed, fmt.Errorf("query restored cluster: %w", err)
	}
	if err := compareCounts(sourceMeta, gotTables, gotVersion); err != nil {
		return nil, nil, backupjobs.ReasonDrillCountsMismatch, err
	}

	meta := map[string]any{
		"source_run_id":       src.ID,
		"source_artifact_key": src.Key,
		"tables":              gotTables,
		"schema_version":      gotVersion,
	}
	return nil, meta, "", nil
}

// download streams the artifact into path and returns the sha256 of what
// actually arrived — the hex the digest check compares against the record.
func (j *DrillJob) download(ctx context.Context, key, path string) (string, error) {
	obj, err := j.store.OpenObject(ctx, key)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", key, err)
	}
	defer obj.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path inside our own MkdirTemp
	if err != nil {
		return "", fmt.Errorf("create download spool: %w", err)
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, hasher), obj)
	closeErr := out.Close()
	if copyErr != nil {
		return "", fmt.Errorf("download %s: %w", key, copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("flush download spool: %w", closeErr)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// decrypt turns the spooled ciphertext into the pg_restore input. A plaintext
// artifact (explicit BACKUP_ALLOW_PLAINTEXT deployments, ".dump" with no
// ".age") passes through untouched.
func (j *DrillJob) decrypt(src *backupjobs.DumpRunRef, spoolPath, dumpPath string) (string, error) {
	if !strings.HasSuffix(src.Key, ".age") {
		return spoolPath, nil
	}
	if len(j.identities) == 0 {
		return "", fmt.Errorf("artifact %s is age-encrypted and no age identity is configured (BACKUP_AGE_IDENTITY_FILE or BACKUP_AGE_IDENTITY) — the drill cannot open it", src.Key)
	}
	in, err := os.Open(spoolPath) // #nosec G304 -- path inside our own MkdirTemp
	if err != nil {
		return "", fmt.Errorf("reopen spool: %w", err)
	}
	defer in.Close()
	plain, err := age.Decrypt(in, j.identities...)
	if err != nil {
		return "", fmt.Errorf("age decrypt %s: %w", src.Key, err)
	}
	out, err := os.OpenFile(dumpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path inside our own MkdirTemp
	if err != nil {
		return "", fmt.Errorf("create decrypted dump: %w", err)
	}
	_, copyErr := io.Copy(out, plain)
	closeErr := out.Close()
	if copyErr != nil {
		// age authenticates per chunk; tampering surfaces here, on read.
		return "", fmt.Errorf("age decrypt %s: %w", src.Key, copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("flush decrypted dump: %w", closeErr)
	}
	return dumpPath, nil
}

// exec runs one drill step through the command seam. The error carries the
// tool's first stderr line for the LOG only — the normalized reason is what
// reaches backup_run (stderr can carry paths and connection details).
func (j *DrillJob) exec(ctx context.Context, name string, args ...string) error {
	cmd := j.command(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w (stderr: %s)", name, err, firstLine(stderr.String()))
	}
	return nil
}

// rowQuerier is the one query shape both count sides share: the source pool
// (dump) and the single restored-cluster connection (drill).
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// collectSanityCounts reads the sanity yardstick: rows per content table plus
// schema_migrations.version. Table names come from the compiled sanityTables
// list only — never from data.
func collectSanityCounts(ctx context.Context, q rowQuerier) (map[string]int64, int64, error) {
	counts := make(map[string]int64, len(sanityTables))
	for _, table := range sanityTables {
		var n int64
		if err := q.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			return nil, 0, fmt.Errorf("count %s: %w", table, err)
		}
		counts[table] = n
	}
	var version int64
	if err := q.QueryRow(ctx, `SELECT version FROM schema_migrations`).Scan(&version); err != nil {
		return nil, 0, fmt.Errorf("schema version: %w", err)
	}
	return counts, version, nil
}

// snapshotSanityCounts is the production side of DumpJob.snapshotCounts: a
// REPEATABLE READ read-only transaction whose exported snapshot pg_dump then
// attaches to, so counts and archive describe the same instant. The returned
// release rolls the transaction back; the caller holds it open until pg_dump
// exits.
func snapshotSanityCounts(ctx context.Context, pool *pgxpool.Pool) (map[string]int64, int64, string, func(), error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, "", nil, fmt.Errorf("begin snapshot tx: %w", err)
	}
	release := func() { _ = tx.Rollback(context.Background()) }
	var snapshotID string
	if err := tx.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&snapshotID); err != nil {
		release()
		return nil, 0, "", nil, fmt.Errorf("export snapshot: %w", err)
	}
	tables, version, err := collectSanityCounts(ctx, tx)
	if err != nil {
		release()
		return nil, 0, "", nil, err
	}
	return tables, version, snapshotID, release, nil
}

// queryRestoredCounts connects to the ephemeral cluster over its unix socket
// and reads the same yardstick from the restored side.
func queryRestoredCounts(ctx context.Context, socketDir, database, user string) (map[string]int64, int64, error) {
	conn, err := pgx.Connect(ctx, fmt.Sprintf("host=%s port=%s user=%s dbname=%s sslmode=disable", socketDir, drillPort, user, database))
	if err != nil {
		return nil, 0, fmt.Errorf("connect restored cluster: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	return collectSanityCounts(ctx, conn)
}

// parseDumpMeta turns the JSONB map a dump row carries into DumpMeta.
// Missing tables is a typed empty (schema-only degrade); any other tables
// shape is an error — the old type-assert to map[string]any fail-opened.
func parseDumpMeta(raw map[string]any) (DumpMeta, error) {
	if raw == nil {
		return DumpMeta{}, nil
	}
	var meta DumpMeta
	if v, ok := raw["encrypted"].(bool); ok {
		meta.Encrypted = v
	}
	if v, ok := raw["schema_version"]; ok {
		if n, ok := metaInt(v); ok {
			meta.SchemaVersion = n
		}
	}
	if v, ok := raw["prune_error"].(string); ok {
		meta.PruneError = v
	}
	if v, ok := raw["tables"]; ok && v != nil {
		tables, err := parseDumpTables(v)
		if err != nil {
			return DumpMeta{}, err
		}
		meta.Tables = tables
	}
	return meta, nil
}

func parseDumpTables(v any) (map[string]int64, error) {
	switch t := v.(type) {
	case map[string]int64:
		return t, nil
	case map[string]any:
		out := make(map[string]int64, len(t))
		for table, raw := range t {
			n, ok := metaInt(raw)
			if !ok {
				return nil, fmt.Errorf("source meta count for %s is not a number", table)
			}
			out[table] = n
		}
		return out, nil
	default:
		return nil, fmt.Errorf("source meta tables is %T, want a map of table counts", v)
	}
}

// compareCounts is the drill's verdict: the restored database must hold the
// row counts and the schema version the source dump recorded in its meta.
// Pure on purpose — the stubbed cluster of the unit tests cannot answer SQL,
// so the comparison logic is tested directly. A dump whose meta predates the
// counts (or shipped without them) is compared on schema version alone.
func compareCounts(source DumpMeta, gotTables map[string]int64, gotVersion int64) error {
	if source.SchemaVersion != 0 && source.SchemaVersion != gotVersion {
		return fmt.Errorf("restored schema_migrations.version is %d, source recorded %d", gotVersion, source.SchemaVersion)
	}
	if len(source.Tables) == 0 {
		return nil
	}
	for table, wanted := range source.Tables {
		got, present := gotTables[table]
		if !present {
			return fmt.Errorf("table %s was counted at dump time but is absent from the restored database", table)
		}
		if got != wanted {
			return fmt.Errorf("table %s restored %d rows, source recorded %d", table, got, wanted)
		}
	}
	return nil
}

// metaInt coerces the numeric shapes a JSONB round-trip can produce.
func metaInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

// sweepOrphanDrillDirs removes leftover drill directories from runs that
// died without their defer (SIGKILL, OOM). Best-effort by design: a sweep
// that cannot delete logs and moves on — the drill itself must not fail
// over its predecessor's corpse.
func sweepOrphanDrillDirs(spoolDir string, logger *slog.Logger) {
	base := spoolDir
	if base == "" {
		base = os.TempDir()
	}
	orphans, err := filepath.Glob(filepath.Join(base, "foldex-drill-*"))
	if err != nil {
		return
	}
	for _, orphan := range orphans {
		if err := os.RemoveAll(orphan); err != nil {
			logger.Warn("could not sweep orphan drill dir", "dir", orphan, "err", err)
		} else {
			logger.Info("swept orphan drill dir", "dir", orphan)
		}
	}
}

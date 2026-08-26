package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

// RestoreAdvisoryLockKey is the process-wide pg advisory lock for Restore so
// two concurrent restores cannot interleave wipe/insert (RACE-HER-007). Chosen
// as a stable int64 unrelated to row ids ('FOLDXRST' as hex-ish constant).
// Exported so integration tests can hold the lock and assert 409.
const RestoreAdvisoryLockKey int64 = 0x464F4C4458525354

// InstanceBackupAdvisoryLockKey serializes the operational backup agent's jobs
// (ADR-43) against each other across processes — 'FOLDXBKP' in the same
// mnemonic scheme. It lives here rather than in the agent package so both
// sides of the coordination read their keys from one file: the agent also
// PROBES RestoreAdvisoryLockKey before jobs that read the bucket, because a
// per-user restore in flight leaves RustFS mid-write (the database is
// transactional, the bucket is not — INV-104).
const InstanceBackupAdvisoryLockKey int64 = 0x464F4C4458424B50

// StorageBucket is the contract the backup module needs from object storage.
// Kept narrow so tests can mock it without standing up RustFS.
type StorageBucket interface {
	WalkObjects(ctx context.Context, prefix string, visit func(ObjectInfo) error) error
	OpenObject(ctx context.Context, key string) (io.ReadCloser, error)
	PutObjectStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	ExistingObjects(ctx context.Context, keys []string) (map[string]bool, error)
	// DeleteObjects removes only the explicit keys proved owner-scoped by the
	// database. Prefix deletion is forbidden because bucket keys are flat.
	DeleteObjects(ctx context.Context, keys []string) error
}

type ObjectInfo struct {
	Key  string
	Size int64
}

// File prefixes inside the bucket that backups should cover. "notes/" holds
// inline images uploaded through the note rich-text editor.
var bucketPrefixes = []string{"screenshots/", "images/", "notes/"}

// foldexVersion is overridden at build time via -ldflags. Empty string means
// "unknown" and is left out of the manifest.
var foldexVersion = ""

type Service struct {
	pool    *pgxpool.Pool
	storage StorageBucket
	logger  *slog.Logger
}

func NewService(pool *pgxpool.Pool, storage StorageBucket, logger *slog.Logger) *Service {
	return &Service{pool: pool, storage: storage, logger: logger}
}

// ────────────────────────────────────────────────────────────────────────────
// Export — produces the ZIP.

// Export streams a full backup ZIP into w. Counts are known after the
// snapshot read and the bucket listing complete — onCountsReady (optional) is
// invoked at that point so HTTP callers can flush response headers BEFORE the
// first byte of zip data hits the wire. Returning an error from onCountsReady
// aborts the export; returning nil lets it proceed. nil = no callback.
//
// Memory profile: O(owner-file-count) for owner keys, selected metadata, and
// manifest checksums, explicitly bounded by archive entry/manifest limits.
// Global bucket metadata and DB rows are visited one at a time. database.json
// lives in a bounded temp file so a slow HTTP client never holds a DB tx.
func (s *Service) Export(ctx context.Context, uid authctx.UserID, w io.Writer, onCountsReady func(Counts) error) (ExportReport, error) {
	start := time.Now()
	var rep ExportReport

	// Pull a consistent snapshot under REPEATABLE READ so the 5 SELECTs and
	// the object-store listing all see the same point in time. The tx is committed
	// as soon as the snapshot + bucket listings finish — keeping it open
	// across the actual ZIP stream would let a slow client peg WAL retention
	// and trip Postgres' idle_in_transaction_session_timeout on multi-GB
	// downloads.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return rep, fmt.Errorf("backup: begin tx: %w", err)
	}
	// Use a flag so the deferred rollback skips when we've already committed.
	// A double-Rollback is a no-op in pgx, but the explicit flag documents
	// the intent for readers who don't know that.
	txDone := false
	defer func() {
		if !txDone {
			_ = tx.Rollback(ctx)
		}
	}()

	spool, err := createSnapshotSpool(ctx, tx, uid)
	if err != nil {
		return rep, fmt.Errorf("backup: spool snapshot: %w", err)
	}
	defer spool.cleanup()

	// Which of the bucket's objects belong to the caller. Keys are FLAT
	// ({prefix}/{id}.ext, no tenant segment), so the prefix listing alone cannot
	// tell whose file is whose — exporting every listed key would put every
	// other tenant's screenshots and note images inside a ZIP the caller
	// downloads. Ownership is established from the caller's own rows.
	//
	// Consequence worth stating: an object nobody references — a leftover from a
	// deleted link, say — is attributable to no one and therefore no longer
	// travels in the backup. That is the correct trade for a flat key space; the
	// alternative is re-keying every object under a tenant segment, which means
	// rewriting og_image_url on every row and moving live objects.
	owned, err := userObjectKeys(ctx, tx, uid, false)
	if err != nil {
		return rep, fmt.Errorf("backup: enumerate own objects: %w", err)
	}
	ownedSet := make(map[string]struct{}, len(owned))
	for _, k := range owned {
		ownedSet[k] = struct{}{}
	}

	listing, err := listOwnedObjects(ctx, s.storage, ownedSet, spool.size)
	if err != nil {
		return rep, err
	}

	// Snapshot is fully captured; the tx no longer needs to be held while we
	// stream bytes to the client. Commit (read-only tx — semantically the
	// same as rollback for visibility) and release the WAL hold.
	if err := tx.Commit(ctx); err != nil {
		return rep, fmt.Errorf("backup: commit snapshot tx: %w", err)
	}
	txDone = true

	counts := spool.counts
	counts.Files = listing.count
	counts.FileBytes = listing.bytes

	if onCountsReady != nil {
		if err := onCountsReady(counts); err != nil {
			return rep, fmt.Errorf("backup: header hook: %w", err)
		}
	}

	zw := zip.NewWriter(w)
	checksums := map[string]string{}

	dbWriter, err := zw.CreateHeader(&zip.FileHeader{Name: "database.json", Method: zip.Deflate})
	if err != nil {
		return rep, fmt.Errorf("backup: zip create database.json: %w", err)
	}
	if _, err := spool.file.Seek(0, io.SeekStart); err != nil {
		return rep, fmt.Errorf("backup: seek database spool: %w", err)
	}
	if _, err := io.Copy(dbWriter, spool.file); err != nil {
		return rep, fmt.Errorf("backup: stream database.json: %w", err)
	}
	checksums["database.json"] = spool.checksum

	// files/
	for _, object := range listing.objects {
		entryName := "files/" + object.Key
		if err := s.streamObjectIntoZip(ctx, zw, entryName, object.Key, object.Size, checksums); err != nil {
			return rep, err
		}
	}

	manifest := Manifest{
		Kind:          ManifestKind,
		Version:       ManifestVersion,
		SchemaVersion: CurrentSchemaVersion,
		CreatedAt:     time.Now().UTC(),
		FoldexVersion: foldexVersion,
		Counts:        counts,
		Checksums:     checksums,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return rep, fmt.Errorf("backup: marshal manifest: %w", err)
	}
	if int64(len(manifestBytes)) > maxManifestJSONBytes {
		return rep, fmt.Errorf("backup: manifest exceeds %d-byte limit", maxManifestJSONBytes)
	}
	// Manifest written last (and intentionally NOT included in `checksums`).
	// Stored uncompressed (Method=Store) so the frontend can extract counts
	// without an inflate step. The size cost is negligible (~few KB).
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Store})
	if err != nil {
		return rep, fmt.Errorf("backup: zip create manifest: %w", err)
	}
	if _, err := mw.Write(manifestBytes); err != nil {
		return rep, fmt.Errorf("backup: zip write manifest: %w", err)
	}

	if err := zw.Close(); err != nil {
		return rep, fmt.Errorf("backup: zip close: %w", err)
	}

	rep.Counts = counts
	rep.DurationMs = time.Since(start).Milliseconds()
	return rep, nil
}

type ownedObjectListing struct {
	objects []ObjectInfo
	count   int64
	bytes   int64
}

func listOwnedObjects(ctx context.Context, storage StorageBucket, owned map[string]struct{}, databaseBytes int64) (ownedObjectListing, error) {
	listing := ownedObjectListing{objects: make([]ObjectInfo, 0, len(owned))}
	selected := make(map[string]struct{}, len(owned))
	manifestIndexBytes := checksumManifestBytes("database.json")
	for _, prefix := range bucketPrefixes {
		err := storage.WalkObjects(ctx, prefix, func(object ObjectInfo) error {
			if _, ok := owned[object.Key]; !ok {
				return nil
			}
			if _, duplicate := selected[object.Key]; duplicate {
				return nil
			}
			if !strings.HasPrefix(object.Key, prefix) || len(object.Key) > maxBackupObjectKeyBytes {
				return fmt.Errorf("object key %q is outside the backup key budget", object.Key)
			}
			if object.Size < 0 || object.Size > maxArchiveFileBytes {
				return fmt.Errorf("object %q has size %d (max %d)", object.Key, object.Size, maxArchiveFileBytes)
			}
			if listing.count >= maxBackupFileEntries {
				return fmt.Errorf("backup has more than %d file entries", maxBackupFileEntries)
			}
			if object.Size > maxArchiveExpandedBytes-maxManifestJSONBytes-databaseBytes-listing.bytes {
				return fmt.Errorf("backup expanded bytes exceed %d-byte limit", maxArchiveExpandedBytes)
			}
			entryName := "files/" + object.Key
			manifestIndexBytes += checksumManifestBytes(entryName)
			if manifestIndexBytes > maxManifestJSONBytes-manifestFixedHeadroom {
				return fmt.Errorf("backup manifest checksum index exceeds %d-byte limit", maxManifestJSONBytes)
			}
			selected[object.Key] = struct{}{}
			listing.objects = append(listing.objects, object)
			listing.count++
			listing.bytes += object.Size
			return nil
		})
		if err != nil {
			return ownedObjectListing{}, fmt.Errorf("backup: list %q: %w", prefix, err)
		}
	}
	return listing, nil
}

func (s *Service) streamObjectIntoZip(ctx context.Context, zw *zip.Writer, entryName, key string, expectedSize int64, checksums map[string]string) error {
	rc, err := s.storage.OpenObject(ctx, key)
	if err != nil {
		return fmt.Errorf("backup: open %q: %w", key, err)
	}
	defer rc.Close()

	header := &zip.FileHeader{Name: entryName, Method: zip.Deflate}
	w, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("backup: zip create %q: %w", entryName, err)
	}

	h := sha256.New()
	tee := io.TeeReader(rc, h)
	n, err := io.Copy(w, io.LimitReader(tee, maxArchiveFileBytes+1))
	if err != nil {
		return fmt.Errorf("backup: copy %q: %w", entryName, err)
	}
	if n > maxArchiveFileBytes {
		return fmt.Errorf("backup: object %q exceeds %d-byte limit", key, maxArchiveFileBytes)
	}
	if n != expectedSize {
		return fmt.Errorf("backup: object %q changed after listing: listed=%d streamed=%d", key, expectedSize, n)
	}
	checksums[entryName] = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return nil
}

func checksumManifestBytes(name string) int64 {
	encodedName, _ := json.Marshal(name)
	// Include separators, quoted hash, newline, and indentation emitted by
	// MarshalIndent. A small overestimate keeps admission ahead of HTTP headers.
	const formattingBytes = 12
	return int64(len(encodedName) + formattingBytes + 2 + len("sha256:") + sha256.Size*2)
}

type snapshotSpool struct {
	file     *os.File
	counts   Counts
	checksum string
	size     int64
}

func createSnapshotSpool(ctx context.Context, tx pgx.Tx, uid authctx.UserID) (*snapshotSpool, error) {
	file, err := os.CreateTemp("", "foldex-backup-database-*.json")
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()

	hash := sha256.New()
	bounded := &boundedWriter{w: io.MultiWriter(file, hash), max: maxDatabaseJSONBytes}
	counts, err := streamSnapshotJSON(ctx, tx, uid, bounded)
	if err != nil {
		return nil, err
	}
	failed = false
	return &snapshotSpool{
		file:     file,
		counts:   counts,
		checksum: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		size:     bounded.written,
	}, nil
}

func (s *snapshotSpool) cleanup() {
	_ = s.file.Close()
	_ = os.Remove(s.file.Name()) // #nosec G703 -- path comes from os.CreateTemp, never from user input
}

type boundedWriter struct {
	w       io.Writer
	written int64
	max     int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.max-w.written {
		return 0, fmt.Errorf("database.json exceeds %d-byte limit", w.max)
	}
	n, err := w.w.Write(p)
	w.written += int64(n)
	return n, err
}

// ────────────────────────────────────────────────────────────────────────────
// Validate — inspects a ZIP without applying it.

func (s *Service) Validate(ctx context.Context, uid authctx.UserID, zr *zip.Reader) (Validation, error) {
	v := Validation{Conflicts: Conflicts{}, Warnings: []string{}, Errors: []string{}}
	preflight := inspectBackupArchive(ctx, zr)
	if err := ctx.Err(); err != nil {
		return v, err
	}
	v.Manifest = preflight.manifest
	v.Warnings = append(v.Warnings, preflight.warnings...)
	v.Errors = append(v.Errors, preflight.errors...)
	if len(v.Errors) > 0 {
		return v, nil
	}

	// Conflict detection against the live DB.
	conflicts, err := countConflicts(ctx, s.pool, uid, preflight.snapshot)
	if err != nil {
		return v, fmt.Errorf("backup: conflicts: %w", err)
	}
	v.Conflicts = conflicts

	v.OK = len(v.Errors) == 0
	return v, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers shared between Export and Validate.

func readManifest(ctx context.Context, archive *inspectedArchive) (*Manifest, error) {
	entry, exists := archive.entries["manifest.json"]
	if !exists {
		return nil, fmt.Errorf("manifest.json missing")
	}
	f, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	defer f.Close()
	raw, err := readAtMost(contextReader{ctx: ctx, reader: f}, maxManifestJSONBytes)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

func readSnapshotFromZip(ctx context.Context, archive *inspectedArchive) (*Snapshot, error) {
	entry, exists := archive.entries["database.json"]
	if !exists {
		return nil, errors.New("database.json missing")
	}
	f, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("open database.json: %w", err)
	}
	defer f.Close()
	limited := &io.LimitedReader{R: contextReader{ctx: ctx, reader: f}, N: maxDatabaseJSONBytes + 1}
	var snap Snapshot
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snap); err != nil {
		if limited.N == 0 {
			return nil, fmt.Errorf("database.json exceeds %d-byte limit", maxDatabaseJSONBytes)
		}
		return nil, fmt.Errorf("parse database.json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if limited.N == 0 {
			return nil, fmt.Errorf("database.json exceeds %d-byte limit", maxDatabaseJSONBytes)
		}
		return nil, fmt.Errorf("parse database.json: trailing data")
	}
	if limited.N == 0 {
		return nil, fmt.Errorf("database.json exceeds %d-byte limit", maxDatabaseJSONBytes)
	}
	if err := validateSnapshotCardinalities(&snap); err != nil {
		return nil, err
	}
	if err := snap.validatePasswordHashes(); err != nil {
		return nil, err
	}
	// The zip is a trust boundary: tag/folder colors come from attacker-
	// controlled input and a `red url("https://evil/exfil")` value would
	// render as a tracking pixel on every chip (CLAUDE.md §4). Coerce BEFORE
	// any restore mode touches the snapshot, so identity/skip/duplicate all
	// inherit the guard. Silently coerces today; a future iteration can
	// surface the returned count as a restore warning.
	snap.Sanitize()
	return &snap, nil
}

func zipEntries(archive *inspectedArchive, prefix string) map[string]bool {
	out := map[string]bool{}
	for name := range archive.entries {
		if strings.HasPrefix(name, prefix) {
			out[name] = true
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

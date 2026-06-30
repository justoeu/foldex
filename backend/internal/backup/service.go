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
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StorageBucket is the contract the backup module needs from object storage.
// Kept narrow so tests can mock it without standing up MinIO.
type StorageBucket interface {
	ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error)
	OpenObject(ctx context.Context, key string) (io.ReadCloser, error)
	PutObjectStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	ObjectExists(ctx context.Context, key string) (bool, error)
	DeleteObjectsPrefix(ctx context.Context, prefix string) error
}

type ObjectInfo struct {
	Key  string
	Size int64
}

// File prefixes inside the bucket that backups should cover. "notes/" holds
// inline images uploaded through the note rich-text editor.
var bucketPrefixes = []string{"screenshots/", "images/", "notes/"}

// maxManifestEntries caps Validate's checksum iteration so a hostile zip can't
// drive unbounded CPU/RAM before sanity checks run.
const maxManifestEntries = 100_000

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
// Memory profile: O(snapshot DB rows) + O(largest single object in the
// bucket). The previous handler buffered the entire ZIP in memory, which made
// a 2 GiB backup a 2 GiB heap allocation; this path streams every entry.
func (s *Service) Export(ctx context.Context, w io.Writer, onCountsReady func(Counts) error) (ExportReport, error) {
	start := time.Now()
	var rep ExportReport

	// Pull a consistent snapshot under REPEATABLE READ so the 5 SELECTs and
	// the MinIO listing all see the same point in time. The tx is committed
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

	snap, err := readSnapshot(ctx, tx)
	if err != nil {
		return rep, fmt.Errorf("backup: read snapshot: %w", err)
	}

	// Pre-list every bucket prefix while we still hold the REPEATABLE READ tx,
	// so file count + bytes are known to the caller before any zip byte is
	// flushed. Object payloads are streamed lazily below — only the metadata
	// is buffered here.
	type objList struct {
		prefix string
		objs   []ObjectInfo
	}
	var lists []objList
	var fileCount, fileBytes int64
	for _, prefix := range bucketPrefixes {
		objs, err := s.storage.ListObjects(ctx, prefix)
		if err != nil {
			return rep, fmt.Errorf("backup: list %q: %w", prefix, err)
		}
		lists = append(lists, objList{prefix: prefix, objs: objs})
		for _, o := range objs {
			fileCount++
			fileBytes += o.Size
		}
	}

	// Snapshot is fully captured; the tx no longer needs to be held while we
	// stream bytes to the client. Commit (read-only tx — semantically the
	// same as rollback for visibility) and release the WAL hold.
	if err := tx.Commit(ctx); err != nil {
		return rep, fmt.Errorf("backup: commit snapshot tx: %w", err)
	}
	txDone = true

	counts := Counts{
		Links:     int64(len(snap.Links)),
		Notes:     int64(len(snap.Notes)),
		Tags:      int64(len(snap.Tags)),
		Folders:   int64(len(snap.Folders)),
		LinkTags:  int64(len(snap.LinkTags)) + int64(len(snap.NoteTags)),
		ClickLogs: int64(len(snap.ClickLogs)) + int64(len(snap.NoteClicks)),
		Files:     fileCount,
		FileBytes: fileBytes,
	}

	if onCountsReady != nil {
		if err := onCountsReady(counts); err != nil {
			return rep, fmt.Errorf("backup: header hook: %w", err)
		}
	}

	zw := zip.NewWriter(w)
	checksums := map[string]string{}

	// database.json
	dbBytes, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return rep, fmt.Errorf("backup: marshal snapshot: %w", err)
	}
	if err := writeZipEntry(zw, "database.json", dbBytes, checksums); err != nil {
		return rep, err
	}

	// files/
	for _, l := range lists {
		for _, o := range l.objs {
			entryName := "files/" + o.Key
			if err := s.streamObjectIntoZip(ctx, zw, entryName, o.Key, checksums); err != nil {
				return rep, err
			}
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

func (s *Service) streamObjectIntoZip(ctx context.Context, zw *zip.Writer, entryName, key string, checksums map[string]string) error {
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
	if _, err := io.Copy(w, tee); err != nil {
		return fmt.Errorf("backup: copy %q: %w", entryName, err)
	}
	checksums[entryName] = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return nil
}

func writeZipEntry(zw *zip.Writer, name string, data []byte, checksums map[string]string) error {
	h := sha256.Sum256(data)
	checksums[name] = "sha256:" + hex.EncodeToString(h[:])
	return writeZipEntryRaw(zw, name, data)
}

func writeZipEntryRaw(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return fmt.Errorf("backup: zip create %q: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("backup: zip write %q: %w", name, err)
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Validate — inspects a ZIP without applying it.

func (s *Service) Validate(ctx context.Context, zr *zip.Reader) (Validation, error) {
	v := Validation{Conflicts: Conflicts{}, Warnings: []string{}, Errors: []string{}}

	manifest, err := readManifest(zr)
	if err != nil {
		v.Errors = append(v.Errors, err.Error())
		return v, nil
	}
	v.Manifest = manifest

	// Magic / version / schema checks.
	if manifest.Kind != ManifestKind {
		v.Errors = append(v.Errors, fmt.Sprintf("kind mismatch: got %q, want %q", manifest.Kind, ManifestKind))
		return v, nil
	}
	majorWant := strings.SplitN(ManifestVersion, ".", 2)[0]
	majorGot := strings.SplitN(manifest.Version, ".", 2)[0]
	if majorGot != majorWant {
		v.Errors = append(v.Errors, fmt.Sprintf("major version mismatch: backup=%s, server=%s", manifest.Version, ManifestVersion))
		return v, nil
	}
	if manifest.SchemaVersion > CurrentSchemaVersion {
		v.Errors = append(v.Errors, fmt.Sprintf("schema_version too new: backup=%d, server=%d", manifest.SchemaVersion, CurrentSchemaVersion))
		return v, nil
	}
	if manifest.SchemaVersion < CurrentSchemaVersion {
		v.Warnings = append(v.Warnings,
			fmt.Sprintf("schema_version do backup (%d) é mais antigo que o atual (%d) — alguns campos serão default.", manifest.SchemaVersion, CurrentSchemaVersion))
	}

	// Cap manifest.Checksums to a sane upper bound — a malicious zip with
	// millions of declared entries would otherwise spin sorting/hashing before
	// any meaningful check ran. 100k is well beyond any realistic foldex
	// backup (typical personal install has at most a few thousand files).
	if len(manifest.Checksums) > maxManifestEntries {
		v.Errors = append(v.Errors, fmt.Sprintf("manifest.checksums has %d entries (max %d) — refusing", len(manifest.Checksums), maxManifestEntries))
		return v, nil
	}

	// Checksum verification (database.json + every files/ entry that the
	// manifest claims a checksum for).
	for _, name := range sortedKeys(manifest.Checksums) {
		want := manifest.Checksums[name]
		entry, err := openEntry(zr, name)
		if err != nil {
			v.Errors = append(v.Errors, fmt.Sprintf("missing entry %q listed in checksums", name))
			continue
		}
		got, err := hashEntry(entry)
		entry.Close()
		if err != nil {
			v.Errors = append(v.Errors, fmt.Sprintf("hash %q: %v", name, err))
			continue
		}
		if got != want {
			v.Errors = append(v.Errors, fmt.Sprintf("checksum mismatch: %s", name))
		}
	}

	// Snapshot parse-only.
	snap, err := readSnapshotFromZip(zr)
	if err != nil {
		v.Errors = append(v.Errors, fmt.Sprintf("database.json: %v", err))
		return v, nil
	}

	// Reference integrity: every link.og_image_url that references /api/files/<key>
	// must have a corresponding entry in files/.
	fileEntries := zipEntries(zr, "files/")
	for _, l := range snap.Links {
		if l.OGImageURL == nil || *l.OGImageURL == "" {
			continue
		}
		key := strings.TrimPrefix(*l.OGImageURL, "/api/files/")
		if key == *l.OGImageURL {
			// External URL — fine.
			continue
		}
		if !fileEntries["files/"+key] {
			v.Warnings = append(v.Warnings, fmt.Sprintf("link %d aponta para %s mas o arquivo não está no ZIP", l.ID, key))
		}
	}

	// Conflict detection against the live DB.
	conflicts, err := countConflicts(ctx, s.pool, snap)
	if err != nil {
		return v, fmt.Errorf("backup: conflicts: %w", err)
	}
	v.Conflicts = conflicts

	v.OK = len(v.Errors) == 0
	return v, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Restore — applies a ZIP.

func (s *Service) Restore(ctx context.Context, zr *zip.Reader, mode ConflictMode) (RestoreReport, error) {
	start := time.Now()
	rep := RestoreReport{Mode: mode, Warnings: []string{}}
	if !mode.Valid() {
		return rep, fmt.Errorf("backup: invalid mode %q", mode)
	}

	manifest, err := readManifest(zr)
	if err != nil {
		return rep, fmt.Errorf("backup: read manifest: %w", err)
	}
	if manifest.Kind != ManifestKind {
		return rep, fmt.Errorf("backup: not a foldex backup")
	}
	if manifest.SchemaVersion > CurrentSchemaVersion {
		return rep, fmt.Errorf("backup: schema_version %d too new (server is %d)", manifest.SchemaVersion, CurrentSchemaVersion)
	}

	snap, err := readSnapshotFromZip(zr)
	if err != nil {
		return rep, fmt.Errorf("backup: parse snapshot: %w", err)
	}

	// DB phase — one tx for atomicity within the DB world.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return rep, fmt.Errorf("backup: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var mapping idMapping
	switch mode {
	case ModeWipe:
		w, err := wipeAll(ctx, tx)
		if err != nil {
			return rep, fmt.Errorf("backup: wipe db: %w", err)
		}
		rep.Wiped = w
		mapping, err = restoreIdentity(ctx, tx, snap)
		if err != nil {
			return rep, fmt.Errorf("backup: insert (wipe): %w", err)
		}
		// Counts inserted = sizes of slices in snap.
		rep.Inserted = Counts{
			Links: int64(len(snap.Links)), Notes: int64(len(snap.Notes)), Tags: int64(len(snap.Tags)),
			Folders:   int64(len(snap.Folders)),
			LinkTags:  int64(len(snap.LinkTags)) + int64(len(snap.NoteTags)),
			ClickLogs: int64(len(snap.ClickLogs)) + int64(len(snap.NoteClicks)),
		}
	case ModeSkip:
		inserted, skipped, m, err := restoreSkip(ctx, tx, snap)
		if err != nil {
			return rep, fmt.Errorf("backup: insert (skip): %w", err)
		}
		rep.Inserted = inserted
		rep.Skipped = skipped
		mapping = m
	case ModeDuplicate:
		inserted, warnings, m, err := restoreDuplicate(ctx, tx, snap)
		if err != nil {
			return rep, fmt.Errorf("backup: insert (duplicate): %w", err)
		}
		rep.Inserted = inserted
		rep.Warnings = append(rep.Warnings, warnings...)
		mapping = m
	}

	if err := tx.Commit(ctx); err != nil {
		return rep, fmt.Errorf("backup: commit: %w", err)
	}

	// Files phase — runs after commit. Idempotent.
	fileRep, err := s.applyFiles(ctx, zr, snap, mapping, mode)
	if err != nil {
		return rep, fmt.Errorf("backup: files: %w", err)
	}
	rep.Files = fileRep

	rep.DurationMs = time.Since(start).Milliseconds()
	return rep, nil
}

func (s *Service) applyFiles(ctx context.Context, zr *zip.Reader, snap *Snapshot, mapping idMapping, mode ConflictMode) (FileReport, error) {
	var rep FileReport
	if mode == ModeWipe {
		for _, prefix := range bucketPrefixes {
			if err := s.storage.DeleteObjectsPrefix(ctx, prefix); err != nil {
				return rep, err
			}
		}
		// Don't bother counting wiped — wipe is bulk.
	}
	for _, entry := range zr.File {
		if !strings.HasPrefix(entry.Name, "files/") {
			continue
		}
		if strings.Contains(entry.Name, "..") {
			return rep, fmt.Errorf("backup: rejected path traversal entry %q", entry.Name)
		}
		key := strings.TrimPrefix(entry.Name, "files/")
		// Defense in depth: even after the `..` check, force the resulting key
		// into one of the known bucket prefixes. Rejects absolute paths, URL-
		// encoded traversals, and any key outside the screenshots/ + images/
		// surface area the rest of the app expects.
		if !hasAllowedPrefix(key) {
			return rep, fmt.Errorf("backup: rejected entry %q (not under %v)", entry.Name, bucketPrefixes)
		}

		// Re-key when mode=duplicate AND we have a mapping for this link id.
		if mode == ModeDuplicate {
			if newKey, ok := mapping.remapFileKey(key); ok {
				key = newKey
			}
		}

		if mode == ModeSkip {
			exists, err := s.storage.ObjectExists(ctx, key)
			if err != nil {
				return rep, err
			}
			if exists {
				rep.Skipped++
				continue
			}
		}

		f, err := entry.Open()
		if err != nil {
			return rep, fmt.Errorf("backup: open zip entry %q: %w", entry.Name, err)
		}
		ct := contentTypeFor(key)
		if err := s.storage.PutObjectStream(ctx, key, f, int64(entry.UncompressedSize64), ct); err != nil {
			f.Close()
			return rep, fmt.Errorf("backup: put %q: %w", key, err)
		}
		f.Close()
		rep.Uploaded++
	}
	return rep, nil
}

func hasAllowedPrefix(key string) bool {
	for _, p := range bucketPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func contentTypeFor(key string) string {
	ext := strings.ToLower(path.Ext(key))
	switch ext {
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

// ────────────────────────────────────────────────────────────────────────────
// Helpers shared between Export and Validate.

func readManifest(zr *zip.Reader) (*Manifest, error) {
	f, err := openEntry(zr, "manifest.json")
	if err != nil {
		return nil, fmt.Errorf("manifest.json missing")
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

func readSnapshotFromZip(zr *zip.Reader) (*Snapshot, error) {
	f, err := openEntry(zr, "database.json")
	if err != nil {
		return nil, errors.New("database.json missing")
	}
	defer f.Close()
	var snap Snapshot
	if err := json.NewDecoder(f).Decode(&snap); err != nil {
		return nil, fmt.Errorf("parse database.json: %w", err)
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

func openEntry(zr *zip.Reader, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("entry %q not found", name)
}

func hashEntry(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func zipEntries(zr *zip.Reader, prefix string) map[string]bool {
	out := map[string]bool{}
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, prefix) {
			out[f.Name] = true
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

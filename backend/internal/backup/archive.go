package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
)

const (
	maxArchiveEntries       = 100_000
	maxManifestJSONBytes    = int64(32 << 20)
	maxDatabaseJSONBytes    = int64(256 << 20)
	maxArchiveFileBytes     = int64(64 << 20)
	maxArchiveExpandedBytes = int64(4 << 30)
	maxBackupFileEntries    = maxArchiveEntries - 2 // database.json + manifest.json
	maxBackupObjectKeyBytes = 1_024                 // S3 object-key limit
	manifestFixedHeadroom   = int64(64 << 10)

	maxSnapshotContentRows  = 250_000
	maxSnapshotRelationRows = 2_000_000
	maxSnapshotSettings     = 1_000
)

type inspectedArchive struct {
	entries map[string]*zip.File
	hashes  map[string]string
	digest  [sha256.Size]byte
}

func inspectArchive(ctx context.Context, zr *zip.Reader) (*inspectedArchive, error) {
	if len(zr.File) > maxArchiveEntries {
		return nil, fmt.Errorf("archive has %d entries (max %d)", len(zr.File), maxArchiveEntries)
	}

	result := &inspectedArchive{
		entries: make(map[string]*zip.File, len(zr.File)),
		hashes:  make(map[string]string, len(zr.File)),
	}
	var declaredBytes int64
	for _, entry := range zr.File {
		if entry == nil {
			return nil, fmt.Errorf("archive contains an invalid entry")
		}
		if _, exists := result.entries[entry.Name]; exists {
			return nil, fmt.Errorf("archive contains duplicate entry %q", entry.Name)
		}
		result.entries[entry.Name] = entry

		limit := archiveEntryLimit(entry.Name)
		if entry.UncompressedSize64 > uint64(limit) {
			return nil, fmt.Errorf("archive entry %q expands to %d bytes (max %d)", entry.Name, entry.UncompressedSize64, limit)
		}
		entryBytes := int64(entry.UncompressedSize64)
		if entryBytes > maxArchiveExpandedBytes-declaredBytes {
			return nil, fmt.Errorf("archive expanded bytes exceed %d-byte limit", maxArchiveExpandedBytes)
		}
		declaredBytes += entryBytes
	}

	var actualBytes int64
	for _, entry := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entryLimit := archiveEntryLimit(entry.Name)
		remaining := maxArchiveExpandedBytes - actualBytes
		readLimit := entryLimit
		if remaining < readLimit {
			readLimit = remaining
		}
		hash, n, err := hashAtMost(ctx, entry, readLimit)
		if err != nil {
			if remaining < entryLimit {
				return nil, fmt.Errorf("archive expanded bytes exceed %d-byte limit", maxArchiveExpandedBytes)
			}
			return nil, fmt.Errorf("archive entry %q: %w", entry.Name, err)
		}
		actualBytes += n
		result.hashes[entry.Name] = hash
	}
	result.digest = archiveDigest(result.hashes)
	return result, nil
}

func archiveDigest(hashes map[string]string) [sha256.Size]byte {
	names := make([]string, 0, len(hashes))
	for name := range hashes {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	var size [8]byte
	for _, name := range names {
		binary.BigEndian.PutUint64(size[:], uint64(len(name)))
		_, _ = h.Write(size[:])
		_, _ = io.WriteString(h, name)
		_, _ = io.WriteString(h, hashes[name])
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func archiveEntryLimit(name string) int64 {
	switch name {
	case "manifest.json":
		return maxManifestJSONBytes
	case "database.json":
		return maxDatabaseJSONBytes
	default:
		return maxArchiveFileBytes
	}
}

func hashAtMost(ctx context.Context, entry *zip.File, max int64) (string, int64, error) {
	r, err := entry.Open()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, readErr := io.Copy(h, io.LimitReader(contextReader{ctx: ctx, reader: r}, max+1))
	closeErr := r.Close()
	if n > max {
		return "", n, fmt.Errorf("expanded data exceeds %d-byte limit", max)
	}
	if readErr != nil {
		return "", n, readErr
	}
	if closeErr != nil {
		return "", n, closeErr
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func readAtMost(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("data exceeds %d-byte limit", max)
	}
	return data, nil
}

type snapshotCollection struct {
	name  string
	count int
	max   int
}

func snapshotCollections(snap *Snapshot) []snapshotCollection {
	return []snapshotCollection{
		{name: "tags", count: len(snap.Tags), max: maxSnapshotContentRows},
		{name: "folders", count: len(snap.Folders), max: maxSnapshotContentRows},
		{name: "links", count: len(snap.Links), max: maxSnapshotContentRows},
		{name: "notes", count: len(snap.Notes), max: maxSnapshotContentRows},
		{name: "link_tags", count: len(snap.LinkTags), max: maxSnapshotRelationRows},
		{name: "note_tags", count: len(snap.NoteTags), max: maxSnapshotRelationRows},
		{name: "click_logs", count: len(snap.ClickLogs), max: maxSnapshotRelationRows},
		{name: "note_clicks", count: len(snap.NoteClicks), max: maxSnapshotRelationRows},
		{name: "app_settings", count: len(snap.AppSettings), max: maxSnapshotSettings},
	}
}

func validateSnapshotCardinalities(snap *Snapshot) error {
	for _, collection := range snapshotCollections(snap) {
		if collection.count > collection.max {
			return fmt.Errorf("database.json collection %q has %d rows (max %d)", collection.name, collection.count, collection.max)
		}
	}
	return nil
}

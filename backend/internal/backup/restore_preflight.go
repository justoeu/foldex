package backup

import (
	"archive/zip"
	"context"
	"fmt"
	"strings"

	"foldex/internal/notemedia"
	"foldex/internal/notes"
)

type backupArchiveInspection struct {
	archive       *inspectedArchive
	manifest      *Manifest
	snapshot      *Snapshot
	warnings      []string
	errors        []string
	unprocessable bool
}

func inspectBackupArchive(ctx context.Context, zr *zip.Reader) backupArchiveInspection {
	var result backupArchiveInspection
	archive, err := inspectArchive(ctx, zr)
	if err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}
	result.archive = archive

	manifest, err := readManifest(ctx, archive)
	if err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}
	result.manifest = manifest
	result.warnings, result.errors, result.unprocessable = validateManifestIntegrity(archive, manifest)
	if len(result.errors) > 0 {
		return result
	}
	if err := validateArchiveFileNames(archive); err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}

	snapshot, err := readSnapshotFromZip(ctx, archive)
	if err != nil {
		result.errors = append(result.errors, fmt.Sprintf("database.json: %v", err))
		return result
	}
	result.snapshot = snapshot
	if err := sanitizeSnapshotNotes(ctx, snapshot); err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}
	warnings, validationErrors, err := validateSnapshotFileReferences(ctx, snapshot, archive)
	if err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}
	result.warnings = append(result.warnings, warnings...)
	result.errors = append(result.errors, validationErrors...)
	result.unprocessable = len(validationErrors) > 0
	return result
}

func validateManifestIntegrity(archive *inspectedArchive, manifest *Manifest) ([]string, []string, bool) {
	var warnings, validationErrors []string
	if manifest.Kind != ManifestKind {
		return warnings, []string{fmt.Sprintf("kind mismatch: got %q, want %q", manifest.Kind, ManifestKind)}, false
	}
	majorWant := strings.SplitN(ManifestVersion, ".", 2)[0]
	majorGot := strings.SplitN(manifest.Version, ".", 2)[0]
	if majorGot != majorWant {
		return warnings, []string{fmt.Sprintf("major version mismatch: backup=%s, server=%s", manifest.Version, ManifestVersion)}, false
	}
	if manifest.SchemaVersion > CurrentSchemaVersion {
		return warnings, []string{fmt.Sprintf("schema_version too new: backup=%d, server=%d", manifest.SchemaVersion, CurrentSchemaVersion)}, false
	}
	if manifest.SchemaVersion < CurrentSchemaVersion {
		warnings = append(warnings,
			fmt.Sprintf("schema_version do backup (%d) é mais antigo que o atual (%d) — alguns campos serão default.", manifest.SchemaVersion, CurrentSchemaVersion))
	}
	if len(manifest.Checksums) > maxArchiveEntries {
		return warnings, []string{fmt.Sprintf("manifest.checksums has %d entries (max %d) — refusing", len(manifest.Checksums), maxArchiveEntries)}, false
	}
	for _, name := range sortedKeys(archive.hashes) {
		if name != "database.json" && !strings.HasPrefix(name, "files/") {
			continue
		}
		if _, exists := manifest.Checksums[name]; !exists {
			validationErrors = append(validationErrors, fmt.Sprintf("missing checksum: %s", name))
		}
	}
	for _, name := range sortedKeys(manifest.Checksums) {
		want := manifest.Checksums[name]
		got, exists := archive.hashes[name]
		if !exists {
			validationErrors = append(validationErrors, fmt.Sprintf("missing entry %q listed in checksums", name))
			continue
		}
		if got != want {
			validationErrors = append(validationErrors, fmt.Sprintf("checksum mismatch: %s", name))
		}
	}
	return warnings, validationErrors, len(validationErrors) > 0
}

func validateArchiveFileNames(archive *inspectedArchive) error {
	for name := range archive.entries {
		if !strings.HasPrefix(name, "files/") {
			continue
		}
		key := strings.TrimPrefix(name, "files/")
		if strings.Contains(name, "..") {
			return fmt.Errorf("backup: rejected path traversal entry %q", name)
		}
		if !hasAllowedPrefix(key) {
			return fmt.Errorf("backup: rejected entry %q (not under %v)", name, bucketPrefixes)
		}
	}
	return nil
}

func sanitizeSnapshotNotes(ctx context.Context, snapshot *Snapshot) error {
	for i := range snapshot.Notes {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot.Notes[i].BodyHTML, snapshot.Notes[i].BodyText = notes.SanitizeBody(snapshot.Notes[i].BodyHTML)
	}
	return nil
}

func validateSnapshotFileReferences(ctx context.Context, snapshot *Snapshot, archive *inspectedArchive) ([]string, []string, error) {
	fileEntries := zipEntries(archive, "files/")
	warnings, err := missingLinkFileWarnings(ctx, snapshot, fileEntries)
	if err != nil {
		return nil, nil, err
	}
	validationErrors, err := missingNoteMediaErrors(ctx, snapshot, fileEntries)
	return warnings, validationErrors, err
}

func missingLinkFileWarnings(ctx context.Context, snapshot *Snapshot, fileEntries map[string]bool) ([]string, error) {
	var warnings []string
	for _, link := range snapshot.Links {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if link.OGImageURL == nil || *link.OGImageURL == "" {
			continue
		}
		key := strings.TrimPrefix(*link.OGImageURL, "/api/files/")
		if key != *link.OGImageURL && !fileEntries["files/"+key] {
			warnings = append(warnings, fmt.Sprintf("link %d aponta para %s mas o arquivo não está no ZIP", link.ID, key))
		}
	}
	return warnings, nil
}

func missingNoteMediaErrors(ctx context.Context, snapshot *Snapshot, fileEntries map[string]bool) ([]string, error) {
	var validationErrors []string
	for _, note := range snapshot.Notes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values := []string{note.BodyHTML}
		if note.CoverURL != nil {
			values = append(values, *note.CoverURL)
		}
		for _, key := range notemedia.Keys(values...) {
			if !fileEntries["files/"+key] {
				validationErrors = append(validationErrors,
					fmt.Sprintf("missing note media: note %d references %s but files/%s is absent", note.ID, key, key))
			}
		}
	}
	return validationErrors, nil
}

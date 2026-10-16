package backup

import (
	"archive/zip"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestIdentityErrors_EveryBranch(t *testing.T) {
	base := func() *Manifest {
		return &Manifest{Kind: ManifestKind, Version: ManifestVersion, SchemaVersion: CurrentSchemaVersion}
	}

	t.Run("valid manifest", func(t *testing.T) {
		warnings, fatal := manifestIdentityErrors(base())
		assert.Empty(t, warnings)
		assert.Empty(t, fatal)
	})

	t.Run("kind mismatch", func(t *testing.T) {
		m := base()
		m.Kind = "other.backup"
		_, fatal := manifestIdentityErrors(m)
		require.Len(t, fatal, 1)
		assert.Contains(t, fatal[0], "kind mismatch")
	})

	t.Run("major version mismatch", func(t *testing.T) {
		m := base()
		m.Version = "9.0"
		_, fatal := manifestIdentityErrors(m)
		require.Len(t, fatal, 1)
		assert.Contains(t, fatal[0], "major version mismatch")
	})

	t.Run("schema too new is fatal", func(t *testing.T) {
		m := base()
		m.SchemaVersion = CurrentSchemaVersion + 1
		_, fatal := manifestIdentityErrors(m)
		require.Len(t, fatal, 1)
		assert.Contains(t, fatal[0], "schema_version too new")
	})

	t.Run("schema older only warns", func(t *testing.T) {
		m := base()
		m.SchemaVersion = CurrentSchemaVersion - 1
		warnings, fatal := manifestIdentityErrors(m)
		assert.Empty(t, fatal)
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "mais antigo")
	})

	t.Run("checksum index beyond entry cap", func(t *testing.T) {
		m := base()
		m.Checksums = make(map[string]string, maxArchiveEntries+1)
		for i := range maxArchiveEntries + 1 {
			m.Checksums[fmt.Sprintf("entry-%d", i)] = "x"
		}
		_, fatal := manifestIdentityErrors(m)
		require.Len(t, fatal, 1)
		assert.Contains(t, fatal[0], "manifest.checksums")
	})
}

func TestManifestChecksumErrors_Branches(t *testing.T) {
	archive := &inspectedArchive{hashes: map[string]string{
		snapshotDBName:       "sha256:db",
		"files/images/1.jpg": "sha256:one",
		"files/images/2.jpg": "sha256:two",
		"manifest.json":      "sha256:ignored",
	}}
	manifest := &Manifest{Checksums: map[string]string{
		snapshotDBName:       "sha256:db",
		"files/images/1.jpg": "sha256:WRONG",
		"files/images/9.jpg": "sha256:ghost",
	}}
	got := manifestChecksumErrors(archive, manifest)
	joined := strings.Join(got, "\n")
	assert.Contains(t, joined, "missing checksum: files/images/2.jpg")
	assert.Contains(t, joined, "checksum mismatch: files/images/1.jpg")
	assert.Contains(t, joined, `missing entry "files/images/9.jpg"`)
	assert.NotContains(t, joined, "manifest.json", "the manifest itself is not checksummed")

	ok := &Manifest{Checksums: map[string]string{snapshotDBName: "sha256:db"}}
	empty := &inspectedArchive{hashes: map[string]string{snapshotDBName: "sha256:db"}}
	assert.Empty(t, manifestChecksumErrors(empty, ok))
}

func TestValidateManifestIntegrity_FatalShortCircuitsChecksums(t *testing.T) {
	archive := &inspectedArchive{hashes: map[string]string{snapshotDBName: "sha256:db"}}
	bad := &Manifest{Kind: "nope"}
	_, errs, unprocessable := validateManifestIntegrity(archive, bad)
	require.Len(t, errs, 1)
	assert.False(t, unprocessable)

	good := &Manifest{Kind: ManifestKind, Version: ManifestVersion, SchemaVersion: CurrentSchemaVersion,
		Checksums: map[string]string{snapshotDBName: "sha256:WRONG"}}
	_, errs, unprocessable = validateManifestIntegrity(archive, good)
	require.NotEmpty(t, errs)
	assert.True(t, unprocessable, "checksum failures are unprocessable, not merely invalid")
}

func TestValidateArchiveFileNames_Branches(t *testing.T) {
	require.NoError(t, validateArchiveFileNames(archiveWithNames(
		"manifest.json", snapshotDBName, "files/images/1.jpg", "files/notes/a.png", "files/screenshots/2.jpg",
	)))

	err := validateArchiveFileNames(archiveWithNames("files/../secret"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")

	err = validateArchiveFileNames(archiveWithNames("files/etc/passwd"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected entry")

	require.NoError(t, validateArchiveFileNames(archiveWithNames("random.txt")),
		"entries outside files/ are not this check's concern")
}

func archiveWithNames(names ...string) *inspectedArchive {
	a := &inspectedArchive{entries: make(map[string]*zip.File, len(names))}
	for _, n := range names {
		a.entries[n] = nil
	}
	return a
}

func TestSanitizeSnapshotNotes_AndCancellation(t *testing.T) {
	snap := &Snapshot{Notes: []NoteRow{{BodyHTML: `<p onclick="x()">hi</p><script>evil()</script>`}}}
	require.NoError(t, sanitizeSnapshotNotes(context.Background(), snap))
	assert.NotContains(t, snap.Notes[0].BodyHTML, "script")
	assert.NotContains(t, snap.Notes[0].BodyHTML, "onclick")
	assert.Contains(t, snap.Notes[0].BodyText, "hi")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sanitizeSnapshotNotes(ctx, &Snapshot{Notes: []NoteRow{{BodyHTML: "<p>x</p>"}}})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMissingLinkFileWarnings_Branches(t *testing.T) {
	ctx := context.Background()
	local := "/api/files/images/5.jpg"
	external := "https://cdn.example/images/5.jpg"
	empty := ""
	snap := &Snapshot{Links: []LinkRow{
		{ID: 1, OGImageURL: &local},
		{ID: 2, OGImageURL: &external},
		{ID: 3, OGImageURL: &empty},
		{ID: 4},
	}}
	warnings, err := missingLinkFileWarnings(ctx, snap, map[string]bool{})
	require.NoError(t, err)
	require.Len(t, warnings, 1, "only the missing local object warns")
	assert.Contains(t, warnings[0], "images/5.jpg")

	warnings, err = missingLinkFileWarnings(ctx, snap, map[string]bool{"files/images/5.jpg": true})
	require.NoError(t, err)
	assert.Empty(t, warnings)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = missingLinkFileWarnings(cancelled, snap, nil)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMissingNoteMediaErrors_Branches(t *testing.T) {
	ctx := context.Background()
	cover := "/api/files/notes/cover-1.png"
	snap := &Snapshot{Notes: []NoteRow{
		{ID: 7, BodyHTML: `<img src="/api/files/notes/body-1.jpg">`, CoverURL: &cover},
		{ID: 8, BodyHTML: "<p>no media</p>"},
	}}
	errs, err := missingNoteMediaErrors(ctx, snap, map[string]bool{"files/notes/body-1.jpg": true})
	require.NoError(t, err)
	require.Len(t, errs, 1, "the cover is missing from the archive")
	assert.Contains(t, errs[0], "cover-1.png")

	errs, err = missingNoteMediaErrors(ctx, snap, map[string]bool{
		"files/notes/body-1.jpg": true, "files/notes/cover-1.png": true,
	})
	require.NoError(t, err)
	assert.Empty(t, errs)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = missingNoteMediaErrors(cancelled, snap, nil)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestValidateSnapshotFileReferences_CombinesBothChecks(t *testing.T) {
	ctx := context.Background()
	local := "/api/files/images/5.jpg"
	snap := &Snapshot{
		Links: []LinkRow{{ID: 1, OGImageURL: &local}},
		Notes: []NoteRow{{ID: 2, BodyHTML: `<img src="/api/files/notes/x.jpg">`}},
	}
	archive := archiveWithNames(snapshotDBName, "manifest.json")
	warnings, errs, err := validateSnapshotFileReferences(ctx, snap, archive)
	require.NoError(t, err)
	assert.Len(t, warnings, 1)
	assert.Len(t, errs, 1)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = validateSnapshotFileReferences(cancelled, snap, archive)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestInspectBackupArchive_CorruptInputs(t *testing.T) {
	ctx := context.Background()

	t.Run("missing manifest", func(t *testing.T) {
		zr := zipReaderWithEntries(t, struct {
			name string
			body []byte
		}{snapshotDBName, []byte(`{"version":7}`)})
		got := inspectBackupArchive(ctx, zr)
		require.NotEmpty(t, got.errors)
		assert.Contains(t, strings.Join(got.errors, " "), "manifest.json missing")
	})

	t.Run("invalid manifest JSON", func(t *testing.T) {
		zr := zipReaderWithEntries(t,
			struct {
				name string
				body []byte
			}{"manifest.json", []byte(`{not json`)},
			struct {
				name string
				body []byte
			}{snapshotDBName, []byte(`{"version":7}`)},
		)
		got := inspectBackupArchive(ctx, zr)
		require.NotEmpty(t, got.errors)
		assert.Contains(t, strings.Join(got.errors, " "), "parse manifest")
	})

	t.Run("wrong kind stops before snapshot", func(t *testing.T) {
		zr := zipReaderWithEntries(t,
			struct {
				name string
				body []byte
			}{"manifest.json", []byte(`{"kind":"other","version":"1.0","schema_version":14,"checksums":{}}`)},
			struct {
				name string
				body []byte
			}{snapshotDBName, []byte(`{"version":7}`)},
		)
		got := inspectBackupArchive(ctx, zr)
		require.NotEmpty(t, got.errors)
		assert.Nil(t, got.snapshot)
	})

	t.Run("traversal file entry rejected after integrity", func(t *testing.T) {
		zr, _ := validBackupReader(t, []byte(`{"version":7}`), struct {
			name string
			body []byte
		}{"files/../evil", []byte("x")})
		got := inspectBackupArchive(ctx, zr)
		require.NotEmpty(t, got.errors)
		assert.Contains(t, strings.Join(got.errors, " "), "traversal")
	})

	t.Run("invalid database.json", func(t *testing.T) {
		zr, _ := validBackupReader(t, []byte(`{corrupt`))
		got := inspectBackupArchive(ctx, zr)
		require.NotEmpty(t, got.errors)
		assert.Contains(t, strings.Join(got.errors, " "), "database.json")
	})

	t.Run("missing note media is unprocessable", func(t *testing.T) {
		db := []byte(`{"version":7,"notes":[{"id":1,"title":"n","body_html":"<img src=\"/api/files/notes/gone.jpg\">"}]}`)
		zr, _ := validBackupReader(t, db)
		got := inspectBackupArchive(ctx, zr)
		require.NotEmpty(t, got.errors)
		assert.True(t, got.unprocessable)
		assert.NotNil(t, got.snapshot)
	})

	t.Run("valid minimal archive passes clean", func(t *testing.T) {
		zr, _ := validBackupReader(t, []byte(`{"version":7,"tags":[],"folders":[],"links":[],"notes":[]}`))
		got := inspectBackupArchive(ctx, zr)
		assert.Empty(t, got.errors)
		require.NotNil(t, got.snapshot)
		require.NotNil(t, got.manifest)
	})
}

func TestReadManifest_AndSnapshot_Errors(t *testing.T) {
	ctx := context.Background()

	empty := &inspectedArchive{entries: map[string]*zip.File{}}
	_, err := readManifest(ctx, empty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest.json missing")

	_, err = readSnapshotFromZip(ctx, empty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database.json missing")

	inspected := func(t *testing.T, db []byte) *inspectedArchive {
		t.Helper()
		zr, _ := validBackupReader(t, db)
		a, err := inspectArchive(ctx, zr)
		require.NoError(t, err)
		return a
	}

	_, err = readSnapshotFromZip(ctx, inspected(t, []byte(`{oops`)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse database.json")

	_, err = readSnapshotFromZip(ctx, inspected(t, []byte(`{"version":7,"surprise_field":1}`)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse database.json")

	_, err = readSnapshotFromZip(ctx, inspected(t, []byte(`{"version":7} {"version":8}`)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trailing data")

	_, err = readSnapshotFromZip(ctx, inspected(t, []byte(
		`{"version":7,"folders":[{"id":1,"name":"f","password_hash":"plaintext"}]}`)))
	require.Error(t, err)
	assert.ErrorIs(t, err, errInvalidPasswordHash)

	snap, err := readSnapshotFromZip(ctx, inspected(t, []byte(
		`{"version":7,"tags":[{"id":1,"name":"t","color":"red url(https://evil.example/x)"}]}`)))
	require.NoError(t, err)
	assert.Equal(t, defaultColor, snap.Tags[0].Color, "hostile colors are coerced at the trust boundary")
}

func TestValidatePasswordHashes_Branches(t *testing.T) {
	require.NoError(t, (&Snapshot{}).validatePasswordHashes())

	noHash := &Snapshot{Folders: []FolderRow{{ID: 1}}}
	require.NoError(t, noHash.validatePasswordHashes())

	supported := "$2b$10$" + strings.Repeat("a", 53)
	require.Len(t, supported, 60)
	ok := &Snapshot{Folders: []FolderRow{{ID: 1, PasswordHash: &supported}}}
	require.NoError(t, ok.validatePasswordHashes())

	bad := "hunter2"
	ko := &Snapshot{Folders: []FolderRow{{ID: 1}, {ID: 2, PasswordHash: &bad}}}
	assert.ErrorIs(t, ko.validatePasswordHashes(), errInvalidPasswordHash)
}

func TestZipEntries_AndValidateSnapshotCardinalities(t *testing.T) {
	archive := archiveWithNames("manifest.json", "files/images/1.jpg", "files/notes/a.png")
	got := zipEntries(archive, filesPrefix)
	assert.Equal(t, map[string]bool{"files/images/1.jpg": true, "files/notes/a.png": true}, got)
	assert.Empty(t, zipEntries(archive, "nothing/"))

	require.NoError(t, validateSnapshotCardinalities(&Snapshot{}))
	over := &Snapshot{AppSettings: make([]AppSettingRow, maxSnapshotSettings+1)}
	err := validateSnapshotCardinalities(over)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app_settings")
}

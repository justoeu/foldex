package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/httperr"
)

func zipReaderWithEntries(t *testing.T, entries ...struct {
	name string
	body []byte
}) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		w, err := zw.Create(entry.name)
		require.NoError(t, err)
		_, err = w.Write(entry.body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	return zr
}

func headerOnlyZip(entries ...zip.FileHeader) *zip.Reader {
	files := make([]*zip.File, len(entries))
	for i := range entries {
		files[i] = &zip.File{FileHeader: entries[i]}
	}
	return &zip.Reader{File: files}
}

func TestInspectArchive_AcceptsBoundedArchive(t *testing.T) {
	zr := zipReaderWithEntries(t,
		struct {
			name string
			body []byte
		}{"manifest.json", []byte(`{"kind":"foldex.backup"}`)},
		struct {
			name string
			body []byte
		}{"database.json", []byte(`{"version":7}`)},
		struct {
			name string
			body []byte
		}{"files/images/1.jpg", []byte("image")},
	)

	got, err := inspectArchive(zr)
	require.NoError(t, err)
	assert.Len(t, got.entries, 3)
	assert.Contains(t, got.hashes, "database.json")
}

func TestValidateAndRestore_EnforceSharedArchiveLimits(t *testing.T) {
	duplicate := func() *zip.Reader {
		return zipReaderWithEntries(t,
			struct {
				name string
				body []byte
			}{"manifest.json", []byte(`{}`)},
			struct {
				name string
				body []byte
			}{"manifest.json", []byte(`{}`)},
		)
	}
	tooMany := func() *zip.Reader {
		return &zip.Reader{File: make([]*zip.File, maxArchiveEntries+1)}
	}
	largeManifest := func() *zip.Reader {
		return headerOnlyZip(zip.FileHeader{Name: "manifest.json", UncompressedSize64: uint64(maxManifestJSONBytes + 1)})
	}
	largeDatabase := func() *zip.Reader {
		return headerOnlyZip(zip.FileHeader{Name: "database.json", UncompressedSize64: uint64(maxDatabaseJSONBytes + 1)})
	}
	largeFile := func() *zip.Reader {
		return headerOnlyZip(zip.FileHeader{Name: "files/images/1.jpg", UncompressedSize64: uint64(maxArchiveFileBytes + 1)})
	}
	largeTotal := func() *zip.Reader {
		count := int(maxArchiveExpandedBytes/maxArchiveFileBytes) + 1
		headers := make([]zip.FileHeader, count)
		for i := range headers {
			headers[i] = zip.FileHeader{
				Name:               "files/images/" + strings.Repeat("x", i+1),
				UncompressedSize64: uint64(maxArchiveFileBytes),
			}
		}
		return headerOnlyZip(headers...)
	}
	tooManySettings := func() *zip.Reader {
		snap := Snapshot{
			Version:     DatabaseSnapshotVersion,
			AppSettings: make([]AppSettingRow, maxSnapshotSettings+1),
		}
		db, err := json.Marshal(snap)
		require.NoError(t, err)
		manifest, err := json.Marshal(Manifest{
			Kind:          ManifestKind,
			Version:       ManifestVersion,
			SchemaVersion: CurrentSchemaVersion,
		})
		require.NoError(t, err)
		return zipReaderWithEntries(t,
			struct {
				name string
				body []byte
			}{"manifest.json", manifest},
			struct {
				name string
				body []byte
			}{"database.json", db},
		)
	}

	cases := []struct {
		name     string
		build    func() *zip.Reader
		contains string
	}{
		{name: "duplicate_name", build: duplicate, contains: "duplicate"},
		{name: "entry_count", build: tooMany, contains: "entries"},
		{name: "manifest_bytes", build: largeManifest, contains: "manifest.json"},
		{name: "database_bytes", build: largeDatabase, contains: "database.json"},
		{name: "file_bytes", build: largeFile, contains: "files/images/1.jpg"},
		{name: "expanded_bytes", build: largeTotal, contains: "expanded"},
		{name: "collection_cardinality", build: tooManySettings, contains: "app_settings"},
	}

	svc := &Service{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validation, err := svc.Validate(context.Background(), 1, tc.build())
			require.NoError(t, err)
			assert.False(t, validation.OK)
			require.NotEmpty(t, validation.Errors)
			assert.Contains(t, strings.Join(validation.Errors, "\n"), tc.contains)

			_, err = svc.Restore(context.Background(), 1, tc.build(), ModeSkip)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.contains)
			var httpErr *httperr.Error
			require.True(t, errors.As(err, &httpErr))
			assert.Equal(t, http.StatusBadRequest, httpErr.Status)
			assert.Equal(t, "invalid_backup", httpErr.Code)
		})
	}
}

func TestReadAtMost_UsesMaxPlusOne(t *testing.T) {
	data, err := readAtMost(strings.NewReader("1234"), 3)
	require.Error(t, err)
	assert.Nil(t, data)
	assert.Contains(t, err.Error(), "3-byte limit")

	data, err = readAtMost(strings.NewReader("123"), 3)
	require.NoError(t, err)
	assert.Equal(t, []byte("123"), data)
}

func TestHashAtMost_UsesMaxPlusOne(t *testing.T) {
	zr := zipReaderWithEntries(t, struct {
		name string
		body []byte
	}{"files/images/1.jpg", []byte("1234")})

	_, n, err := hashAtMost(zr.File[0], 3)
	require.Error(t, err)
	assert.EqualValues(t, 4, n)
	assert.Contains(t, err.Error(), "3-byte limit")
}

func TestSnapshotCollections_ListsEveryRestoredCollection(t *testing.T) {
	collections := snapshotCollections(&Snapshot{})
	names := make([]string, 0, len(collections))
	limits := make(map[string]int, len(collections))
	for _, collection := range collections {
		names = append(names, collection.name)
		limits[collection.name] = collection.max
	}
	assert.ElementsMatch(t, []string{
		"tags", "folders", "links", "notes", "link_tags", "note_tags",
		"click_logs", "note_clicks", "app_settings",
	}, names)
	for _, name := range []string{"tags", "folders", "links", "notes"} {
		assert.Equal(t, maxSnapshotContentRows, limits[name])
	}
	for _, name := range []string{"link_tags", "note_tags", "click_logs", "note_clicks"} {
		assert.Equal(t, maxSnapshotRelationRows, limits[name])
	}
	assert.Equal(t, maxSnapshotSettings, limits["app_settings"])
}

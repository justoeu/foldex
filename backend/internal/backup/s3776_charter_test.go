package backup

import (
	"archive/zip"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3776_InspectArchiveAcceptsBoundedAndRefusesHostile(t *testing.T) {
	ok := zipReaderWithEntries(t,
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
	got, err := inspectArchive(context.Background(), ok)
	require.NoError(t, err)
	assert.Len(t, got.entries, 3)
	assert.Contains(t, got.hashes, "database.json")
	assert.Contains(t, got.hashes, "manifest.json")

	dup := zipReaderWithEntries(t,
		struct {
			name string
			body []byte
		}{"manifest.json", []byte(`{}`)},
		struct {
			name string
			body []byte
		}{"manifest.json", []byte(`{}`)},
	)
	_, err = inspectArchive(context.Background(), dup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")

	tooMany := &zip.Reader{File: make([]*zip.File, maxArchiveEntries+1)}
	_, err = inspectArchive(context.Background(), tooMany)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "entries")

	nilEntry := &zip.Reader{File: []*zip.File{nil}}
	_, err = inspectArchive(context.Background(), nilEntry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid entry")
}

// Skip and duplicate restore used to copy INSERT statements. One helper each
// is the lock; a second literal is a drift waiting to happen after extract.
func TestS3776_RestoreModesShareFolderAndNoteInsertSQL(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "db_restore_staged.go", nil, 0)
	require.NoError(t, err)

	folderSQL, noteSQL := 0, 0
	calls := map[string]map[string]int{
		"restoreSkipStaged":      {},
		"restoreDuplicateStaged": {},
		"insertDuplicateRestore": {},
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			if strings.Contains(s, "INSERT INTO folder") {
				folderSQL++
			}
			if strings.Contains(s, "INSERT INTO note") {
				noteSQL++
			}
		case *ast.FuncDecl:
			if n.Body == nil {
				return true
			}
			if _, watched := calls[n.Name.Name]; !watched {
				return true
			}
			ast.Inspect(n.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				calls[n.Name.Name][id.Name]++
				return true
			})
		}
		return true
	})

	require.Equal(t, 1, folderSQL, "one INSERT INTO folder helper — skip and duplicate must share it")
	require.Equal(t, 1, noteSQL, "one INSERT INTO note helper — skip and duplicate must share it")
	require.Greater(t, calls["restoreSkipStaged"]["insertStagedFolders"], 0, "restoreSkipStaged must call insertStagedFolders")
	require.Greater(t, calls["restoreSkipStaged"]["insertStagedNotes"], 0, "restoreSkipStaged must call insertStagedNotes")
	require.Greater(t, calls["insertDuplicateRestore"]["insertStagedFolders"], 0, "insertDuplicateRestore must call insertStagedFolders")
	require.Greater(t, calls["insertDuplicateRestore"]["insertStagedNotes"], 0, "insertDuplicateRestore must call insertStagedNotes")
	require.Greater(t, calls["restoreDuplicateStaged"]["insertDuplicateRestore"], 0, "restoreDuplicateStaged must call insertDuplicateRestore")
}

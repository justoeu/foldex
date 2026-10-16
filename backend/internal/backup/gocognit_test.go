package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/testsupport"
)

func TestCognitiveComplexity_ExportAndInspectArchive(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "service.go", name: "(*Service).Export"},
		{file: "archive.go", name: "inspectArchive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testsupport.CognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
}

func TestCognitiveComplexity_RestoreHelpers(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "restore_helpers.go", name: "spoolNoteMedia"},
		{file: "restore_helpers.go", name: "attachPolymorphicTags"},
		{file: "restore_helpers.go", name: "copyPolymorphicClicks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testsupport.CognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
}

func TestCognitiveComplexity_RestoreDuplicateStagedAndCopy(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "db_restore_staged.go", name: "restoreDuplicateStaged"},
		{file: "db_restore_staged.go", name: "copyRestoreStaging"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testsupport.CognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
}

func TestCognitiveComplexity_LoadRestoreLedgerAndManifestIntegrity(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "restore_ledger.go", name: "loadRestoreLedger"},
		{file: "restore_preflight.go", name: "validateManifestIntegrity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testsupport.CognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
}

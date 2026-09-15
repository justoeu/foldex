package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeRestoreLedgerJSON_AcceptsACompletePayload(t *testing.T) {
	t.Parallel()
	var ledger restoreLedger
	err := decodeRestoreLedgerJSON(
		&ledger,
		[]byte(`{"links":1,"notes":2}`),
		[]byte(`{"tags":3}`),
		[]byte(`["warn"]`),
		[]byte(`{"uploaded":1}`),
	)
	require.NoError(t, err)
	assert.EqualValues(t, 1, ledger.inserted.Links)
	assert.EqualValues(t, 2, ledger.inserted.Notes)
	assert.EqualValues(t, 3, ledger.skipped.Tags)
	assert.Equal(t, []string{"warn"}, ledger.warnings)
}

func TestDecodeRestoreLedgerJSON_EmptyFilesJSONIsOK(t *testing.T) {
	t.Parallel()
	var ledger restoreLedger
	err := decodeRestoreLedgerJSON(&ledger, []byte(`{}`), []byte(`{}`), []byte(`[]`), nil)
	require.NoError(t, err)
}

func TestDecodeRestoreLedgerJSON_RejectsEachCorruptField(t *testing.T) {
	t.Parallel()
	good := []byte(`{}`)
	bad := []byte(`{`)
	cases := []struct {
		name                               string
		inserted, skipped, warnings, files []byte
		want                               string
	}{
		{"inserted", bad, good, []byte(`[]`), nil, "decode restore inserted counts"},
		{"skipped", good, bad, []byte(`[]`), nil, "decode restore skipped counts"},
		{"warnings", good, good, bad, nil, "decode restore warnings"},
		{"files", good, good, []byte(`[]`), bad, "decode restore file report"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var ledger restoreLedger
			err := decodeRestoreLedgerJSON(&ledger, tc.inserted, tc.skipped, tc.warnings, tc.files)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestApplyRestoreEntityMapping_KnownKindsAndUnknown(t *testing.T) {
	t.Parallel()
	ledger := restoreLedger{mapping: newIDMapping()}
	require.NoError(t, applyRestoreEntityMapping(&ledger, "tag", 1, 11))
	require.NoError(t, applyRestoreEntityMapping(&ledger, "folder", 2, 22))
	require.NoError(t, applyRestoreEntityMapping(&ledger, "link", 3, 33))
	require.NoError(t, applyRestoreEntityMapping(&ledger, "note", 4, 44))
	assert.EqualValues(t, 11, ledger.mapping.tagMap[1])
	assert.EqualValues(t, 22, ledger.mapping.folderMap[2])
	assert.EqualValues(t, 33, ledger.mapping.linkMap[3])
	assert.EqualValues(t, 44, ledger.mapping.noteMap[4])
	err := applyRestoreEntityMapping(&ledger, "widget", 5, 55)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown kind "widget"`)
}

package importer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		mode importMode
		ok   bool
	}{
		{"", modeSkip, true},
		{"skip", modeSkip, true},
		{"SKIP", modeSkip, true},
		{"  wipe  ", modeWipe, true},
		{"duplicate", modeDuplicate, true},
		{"Duplicate", modeDuplicate, true},
		{"nope", "", false},
		{"merge", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseMode(tc.in)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.mode, got)
		})
	}
}

func TestParseExcludedFolders(t *testing.T) {
	assert.Empty(t, parseExcludedFolders(""))
	assert.Empty(t, parseExcludedFolders("  ,  , "))

	got := parseExcludedFolders("Work, Personal, ,Work")
	require.Len(t, got, 2)
	_, ok := got["Work"]
	assert.True(t, ok)
	_, ok = got["Personal"]
	assert.True(t, ok)
}

func TestFilterByFolder(t *testing.T) {
	items := []Item{
		{URL: "https://a.example", Title: "a", Folder: strPtr("Work")},
		{URL: "https://b.example", Title: "b", Folder: strPtr("Personal")},
		{URL: "https://c.example", Title: "c"}, // ungrouped
		{URL: "https://d.example", Title: "d", Folder: strPtr("  Work  ")},
	}

	// Empty exclude set is a no-op.
	assert.Equal(t, items, filterByFolder(items, nil))
	assert.Equal(t, items, filterByFolder(items, map[string]struct{}{}))

	out := filterByFolder(items, map[string]struct{}{"Work": {}})
	require.Len(t, out, 2)
	assert.Equal(t, "https://b.example", out[0].URL)
	assert.Equal(t, "https://c.example", out[1].URL)

	// Exact path match only — trimmed folder "  Work  " is NOT "Work".
	out = filterByFolder(items, map[string]struct{}{"Personal": {}, "": {}})
	require.Len(t, out, 2)
	assert.Equal(t, "https://a.example", out[0].URL)
	assert.Equal(t, "https://d.example", out[1].URL)
}

func TestJSONToItems_FolderAndCreatedAt(t *testing.T) {
	folder := "Docs"
	f := JSONFile{
		Links: []JSONLink{
			{URL: "https://x.example", Title: "", Folder: &folder, CreatedAt: "2024-06-01T12:00:00Z"},
			{URL: "https://y.example", Title: "Y", Folder: strPtr("  "), CreatedAt: "not-rfc3339"},
		},
	}
	items := jsonToItems(f)
	require.Len(t, items, 2)
	assert.Equal(t, "https://x.example", items[0].Title)
	require.NotNil(t, items[0].Folder)
	assert.Equal(t, "Docs", *items[0].Folder)
	require.NotNil(t, items[0].CreatedAt)

	assert.Nil(t, items[1].Folder, "blank folder path must be dropped")
	assert.Nil(t, items[1].CreatedAt, "invalid created_at must be ignored")
}

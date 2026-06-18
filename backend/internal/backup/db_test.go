package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func int64Ptr(v int64) *int64 { return &v }

// TestTopoSortFolders_RootsFirst verifies the basic invariant: every parent is
// emitted before its children.
func TestTopoSortFolders_RootsFirst(t *testing.T) {
	in := []FolderRow{
		{ID: 3, ParentID: int64Ptr(2)},
		{ID: 2, ParentID: int64Ptr(1)},
		{ID: 1, ParentID: nil},
	}
	out := topoSortFolders(in)
	pos := map[int64]int{}
	for i, f := range out {
		pos[f.ID] = i
	}
	assert.Less(t, pos[1], pos[2], "1 must come before 2")
	assert.Less(t, pos[2], pos[3], "2 must come before 3")
}

// TestTopoSortFolders_NoLostFolders is the regression test for the slice-
// aliasing bug. Before the fix, `next := remaining[:0]` shared the backing
// array; `append` then overwrote slots the range loop still had to read.
// A tree where multiple folders defer in the first pass exercised that path.
func TestTopoSortFolders_NoLostFolders(t *testing.T) {
	// Construct a deep tree where each pass can only emit one folder:
	//   1 (root) → 2 → 3 → 4 → 5
	// In input order [5,4,3,2,1] every pass has multiple deferrals and the
	// aliasing bug would silently drop folders.
	in := []FolderRow{
		{ID: 5, ParentID: int64Ptr(4)},
		{ID: 4, ParentID: int64Ptr(3)},
		{ID: 3, ParentID: int64Ptr(2)},
		{ID: 2, ParentID: int64Ptr(1)},
		{ID: 1, ParentID: nil},
	}
	out := topoSortFolders(in)
	assert.Len(t, out, 5, "no folders may be dropped")
	seen := map[int64]bool{}
	for _, f := range out {
		seen[f.ID] = true
	}
	for _, want := range []int64{1, 2, 3, 4, 5} {
		assert.True(t, seen[want], "folder %d must be present in output", want)
	}
}

// TestTopoSortFolders_CycleEmitsAll guards against an infinite loop on a
// dangling-parent cycle: every input folder must still appear in the output.
func TestTopoSortFolders_CycleEmitsAll(t *testing.T) {
	// 1 → 2 → 1 (cycle); 3 is dangling (parent = 999, which doesn't exist).
	in := []FolderRow{
		{ID: 1, ParentID: int64Ptr(2)},
		{ID: 2, ParentID: int64Ptr(1)},
		{ID: 3, ParentID: int64Ptr(999)},
	}
	out := topoSortFolders(in)
	assert.Len(t, out, 3, "cycle/dangling folders must still be emitted")
}

func TestTopoSortFolders_EmptyInput(t *testing.T) {
	assert.Empty(t, topoSortFolders(nil))
	assert.Empty(t, topoSortFolders([]FolderRow{}))
}

// TestRemapFileKey covers the id mapping helper used by ModeDuplicate.
func TestRemapFileKey(t *testing.T) {
	m := newIDMapping()
	m.linkMap[123] = 456

	cases := []struct {
		in     string
		out    string
		mapped bool
	}{
		{"screenshots/123.png", "screenshots/456.png", true},
		{"images/123.jpg", "images/456.jpg", true},
		{"screenshots/999.png", "screenshots/999.png", false}, // no mapping
		{"other/123.png", "other/123.png", false},             // unknown prefix
		{"screenshots/notanumber.png", "screenshots/notanumber.png", false},
		{"screenshots/.png", "screenshots/.png", false}, // dot at zero — no id
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := m.remapFileKey(tc.in)
			assert.Equal(t, tc.out, got)
			assert.Equal(t, tc.mapped, ok)
		})
	}
}

func TestRemapFileKey_IdentityWhenSameID(t *testing.T) {
	m := newIDMapping()
	m.linkMap[7] = 7
	got, ok := m.remapFileKey("screenshots/7.png")
	assert.Equal(t, "screenshots/7.png", got)
	assert.False(t, ok, "identity mapping must be reported as no-op")
}

// TestSnapshot_Sanitize is the security-boundary guard: a snapshot loaded
// from an attacker-controlled backup zip can carry `red url("https://evil/exfil")`
// as a tag/folder color, which would render as a tracking pixel on every chip
// (CLAUDE.md §4). Sanitize must coerce every such value to the indigo default
// before any restore mode writes it to the DB.
func TestSnapshot_Sanitize(t *testing.T) {
	s := &Snapshot{
		Tags: []TagRow{
			{ID: 1, Name: "ok-hex", Color: "#abc"},
			{ID: 2, Name: "ok-gradient", Color: "linear-gradient(135deg, #8B85FF, #6366F1)"},
			{ID: 3, Name: "empty", Color: ""},
			{ID: 4, Name: "tracking-pixel", Color: `red url("https://evil/exfil")`},
			{ID: 5, Name: "named", Color: "red"},
		},
		Folders: []FolderRow{
			{ID: 1, Name: "ok", Color: "#aabbcc"},
			{ID: 2, Name: "css-injection", Color: "expression(alert(1))"},
		},
	}
	coerced := s.Sanitize()

	assert.Equal(t, "#abc", s.Tags[0].Color, "valid hex passes through")
	assert.Equal(t, "linear-gradient(135deg, #8B85FF, #6366F1)", s.Tags[1].Color, "valid gradient passes through")
	assert.Equal(t, defaultColor, s.Tags[2].Color, "empty falls back to default")
	assert.Equal(t, defaultColor, s.Tags[3].Color, "tracking-pixel vector MUST be coerced")
	assert.Equal(t, defaultColor, s.Tags[4].Color, "named color coerced")
	assert.Equal(t, "#aabbcc", s.Folders[0].Color, "folder valid hex passes through")
	assert.Equal(t, defaultColor, s.Folders[1].Color, "folder expression() coerced")

	// 4 coercions: tag empty, tag url(), tag named, folder expression().
	// Valid colors (including gradient) pass through untouched.
	assert.Equal(t, 4, coerced, "coerced count must reflect only the actually-changed values")
}

func TestSnapshot_Sanitize_Empty(t *testing.T) {
	s := &Snapshot{}
	assert.Equal(t, 0, s.Sanitize(), "empty snapshot coerces nothing")
}

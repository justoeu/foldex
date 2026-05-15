package links

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"basic ASCII", "Jira Board", "jira-board"},
		{"em dash + uppercase", "Jira Board — INV-1", "jira-board-inv-1"},
		{"accents to ASCII", "Conexão M2M", "conexao-m2m"},
		{"punctuation", "INV-1: Refactor issuing flow", "inv-1-refactor-issuing-flow"},
		{"collapses runs of hyphens", "a----b", "a-b"},
		{"trims leading + trailing hyphens", "  --foo--  ", "foo"},
		{"empty stays empty", "", ""},
		{"only punctuation collapses to empty", "!!!", ""},
		{"only accents", "ñöü", "nou"},
		{"underscores collapse too", "snake_case_thing", "snake-case-thing"},
		{"emoji are stripped", "🎉 Big Win 🎉", "big-win"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Slugify(tc.in))
		})
	}
}

func TestSlugify_LongTitleCappedOnHyphen(t *testing.T) {
	long := "Refactor the issuing flow for cross-border international wire transfers in the post-migration codebase to make sure observability still holds end to end"
	got := Slugify(long)
	assert.LessOrEqual(t, len(got), slugMaxLen, "must respect slugMaxLen")
	assert.False(t, strings.HasSuffix(got, "-"), "should not end with hyphen")
	// All segments are non-empty (collapse worked correctly).
	for _, seg := range strings.Split(got, "-") {
		assert.NotEmpty(t, seg, "all hyphen-segments must be non-empty")
	}
}

func TestSlugIsValid(t *testing.T) {
	cases := []struct {
		slug string
		ok   bool
	}{
		{"jira-board", true},
		{"a", true},
		{"jira-board-inv-1", true},
		{"42-board", true}, // mixed numeric + word OK
		{"", false},
		{"42", false},     // pure digits forbidden — would shadow /go/42
		{"0001", false},   // even with leading zeros, still pure digits
		{"-foo", false},   // leading hyphen
		{"foo-", false},   // trailing hyphen
		{"foo--bar", false}, // double hyphen
		{"Foo", false},    // uppercase
		{"foo bar", false}, // space
		{"foo.bar", false}, // dot
		{"café", false},   // non-ASCII
		{strings.Repeat("a", slugMaxLen), true},     // exactly at cap
		{strings.Repeat("a", slugMaxLen+1), false},  // one over
	}
	for _, tc := range cases {
		t.Run(tc.slug, func(t *testing.T) {
			assert.Equal(t, tc.ok, SlugIsValid(tc.slug))
		})
	}
}

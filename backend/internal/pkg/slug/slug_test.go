package slug

import (
	"context"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Jira Board — INV-1", "jira-board-inv-1"},
		{"Conexão M2M", "conexao-m2m"},
		{"Hello World", "hello-world"},
		{"  Leading and trailing  ", "leading-and-trailing"},
		{"!!!", ""},
		{"foo@bar.com", "foo-bar-com"},
		{"Café Noir", "cafe-noir"},
		{"über cool", "uber-cool"},
		{"a", "a"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got := Slugify(tc.title)
			if got != tc.want {
				t.Fatalf("Slugify(%q) = %q; want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestSlugifyMaxLen(t *testing.T) {
	// Build a title that exceeds MaxLen and verify truncation to ≤80 chars
	long := "this-is-a-very-long-title-that-definitely-exceeds-the-80-character-slug-limit-and-must-be-truncated-correctly"
	got := Slugify(long)
	if len(got) > MaxLen {
		t.Fatalf("slug %q has len %d, must be ≤ %d", got, len(got), MaxLen)
	}
	if got == long {
		t.Fatal("slug was not truncated")
	}
	// Truncation should end on a word boundary, not mid-word
	if got[len(got)-1] == '-' {
		t.Fatal("slug ends with a hyphen")
	}
}

func TestIsValid(t *testing.T) {
	valid := []string{
		"hello-world",
		"a",
		"abc-123",
		"jira-board-inv-1",
	}
	for _, s := range valid {
		t.Run("valid/"+s, func(t *testing.T) {
			if !IsValid(s) {
				t.Fatalf("IsValid(%q) = false; want true", s)
			}
		})
	}

	invalid := []struct {
		s    string
		desc string
	}{
		{"", "empty"},
		{"Hello-World", "uppercase"},
		{"-leading", "leading hyphen"},
		{"trailing-", "trailing hyphen"},
		{"double--hyphen", "double hyphen"},
		{"123", "all digits"},
		{"a_b", "underscore"},
		{"a b", "space"},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.desc, func(t *testing.T) {
			if IsValid(tc.s) {
				t.Fatalf("IsValid(%q) = true; want false", tc.s)
			}
		})
	}
}

func TestUniqueAvailable(t *testing.T) {
	taken := map[string]bool{"foo": true, "foo-2": true}
	got, err := UniqueAvailable(context.Background(), "foo", func(_ context.Context, c string) (bool, error) {
		return taken[c], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "foo-3" {
		t.Fatalf("got %q want foo-3", got)
	}
}

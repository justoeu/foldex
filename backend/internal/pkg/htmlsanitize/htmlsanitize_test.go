package htmlsanitize

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSanitize_StripsHostileInput is the security-boundary guard for note
// bodies — every vector here must be neutralized, mirroring cssvalid_test.go's
// rigor for tag/folder colors.
func TestSanitize_StripsHostileInput(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		notMay []string // substrings that must NOT appear in the output
	}{
		{
			name:   "script tag",
			input:  `<p>hi</p><script>alert(1)</script>`,
			notMay: []string{"<script", "alert(1)"},
		},
		{
			name:   "event handler attribute",
			input:  `<img src="https://example.com/a.jpg" onerror="alert(1)">`,
			notMay: []string{"onerror"},
		},
		{
			name:   "javascript: href",
			input:  `<a href="javascript:alert(1)">click</a>`,
			notMay: []string{"javascript:"},
		},
		{
			name:   "data: image src",
			input:  `<img src="data:image/png;base64,aGVsbG8=" alt="x">`,
			notMay: []string{"data:image"},
		},
		{
			name:   "table not in allowlist",
			input:  `<table><tr><td>cell</td></tr></table>`,
			notMay: []string{"<table", "<tr", "<td"},
		},
		{
			name:   "style attribute",
			input:  `<p style="background:url(javascript:alert(1))">hi</p>`,
			notMay: []string{"style=", "javascript:"},
		},
		{
			name:   "iframe",
			input:  `<iframe src="https://evil.example"></iframe>`,
			notMay: []string{"<iframe"},
		},
		{
			name:   "svg with script",
			input:  `<svg onload="alert(1)"><script>alert(2)</script></svg>`,
			notMay: []string{"<svg", "onload", "<script"},
		},
		{
			name:   "form element",
			input:  `<form action="https://evil.example"><input></form>`,
			notMay: []string{"<form", "<input"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.input)
			for _, bad := range tc.notMay {
				assert.NotContains(t, strings.ToLower(got), strings.ToLower(bad),
					"sanitized output must not contain %q (input: %q, got: %q)", bad, tc.input, got)
			}
		})
	}
}

// TestSanitize_AllowsStarterKitOutput pins the allowlist against false
// positives — legitimate Tiptap StarterKit markup must pass through intact
// (modulo bluemonday's injected rel/target on links).
func TestSanitize_AllowsStarterKitOutput(t *testing.T) {
	input := `<h1>Title</h1><p>Some <strong>bold</strong> and <em>italic</em> and <code>code</code>.</p>` +
		`<ul><li>one</li><li>two</li></ul><blockquote><p>quoted</p></blockquote><hr><br>` +
		`<img src="/api/files/notes/abc123.jpg" alt="a screenshot" width="600" height="400">`

	got := Sanitize(input)

	for _, want := range []string{
		"<h1>Title</h1>",
		"<strong>bold</strong>",
		"<em>italic</em>",
		"<code>code</code>",
		"<li>one</li>",
		"<blockquote>",
		"<hr",
		"<br",
		`src="/api/files/notes/abc123.jpg"`,
		`alt="a screenshot"`,
		`width="600"`,
		`height="400"`,
	} {
		assert.Contains(t, got, want, "legitimate StarterKit markup must survive sanitization")
	}
}

// TestSanitize_LinkGetsSafeRelAndTarget locks the nofollow/noopener/target
// behavior — and that a user-supplied rel/target can't override it, since
// AllowAttrs only allows href on <a>.
func TestSanitize_LinkGetsSafeRelAndTarget(t *testing.T) {
	got := Sanitize(`<a href="https://example.com" rel="opener" target="_self">link</a>`)
	assert.Contains(t, got, `href="https://example.com"`)
	assert.Contains(t, got, `rel="nofollow`, "user-supplied rel must not survive — only the derived nofollow/noreferrer may")
	assert.NotContains(t, got, `rel="opener"`)
	assert.NotContains(t, got, `target="_self"`)
}

func TestSanitize_AllowsMailtoLinks(t *testing.T) {
	got := Sanitize(`<a href="mailto:a@example.com">mail</a>`)
	assert.Contains(t, got, `href="mailto:a@example.com"`)
}

func TestSanitize_EmptyInput(t *testing.T) {
	assert.Equal(t, "", Sanitize(""))
	assert.Equal(t, "", PlainText(""))
}

func TestPlainText_StripsAllMarkup(t *testing.T) {
	got := PlainText(`<h1>Title</h1><p>Hello <strong>world</strong>!</p><script>alert(1)</script>`)
	assert.Equal(t, "TitleHello world!", got)
	assert.NotContains(t, got, "<")
	assert.NotContains(t, got, "alert")
}

func TestPlainText_TrimsWhitespace(t *testing.T) {
	got := PlainText("  <p>  hi  </p>  ")
	assert.Equal(t, "hi", got)
}

// ── rich-text toolbar styles (TextAlign / Color / FontFamily) ──────────────

func TestSanitize_AllowsTextAlign(t *testing.T) {
	got := Sanitize(`<p style="text-align:center">centered</p>`)
	assert.Contains(t, got, "text-align")
	assert.Contains(t, got, "center")
}

func TestSanitize_AllowsColorSpan(t *testing.T) {
	got := Sanitize(`<p><span style="color: #ff0000">red</span></p>`)
	assert.Contains(t, got, "<span")
	assert.Contains(t, got, "color")
	assert.Contains(t, got, "#ff0000")
}

func TestSanitize_AllowsFontFamily(t *testing.T) {
	got := Sanitize(`<span style="font-family: Georgia, serif">serif</span>`)
	assert.Contains(t, got, "font-family")
	assert.Contains(t, got, "Georgia")
}

func TestSanitize_RejectsDangerousStyleValues(t *testing.T) {
	// url() / expression() must never survive in any style value.
	cases := []string{
		`<span style="color: url('https://evil/x')">x</span>`,
		`<span style="font-family: expression(alert(1))">x</span>`,
		`<span style="font-family: url(javascript:alert(1))">x</span>`,
		`<p style="text-align: url(evil)">x</p>`,
		`<span style="background: red; color: red url('https://evil')">x</span>`,
	}
	for _, in := range cases {
		got := Sanitize(in)
		assert.NotContains(t, got, "url(", "url() must be stripped from %q → %q", in, got)
		assert.NotContains(t, got, "expression", "expression() must be stripped from %q → %q", in, got)
	}
}

func TestSanitize_StripsUnlistedStyleProperty(t *testing.T) {
	// A property not in the allowlist (position) must be dropped even when a
	// sibling allowed property (color) is present.
	got := Sanitize(`<span style="position: fixed; color: #abc">x</span>`)
	assert.NotContains(t, got, "position")
}

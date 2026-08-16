package preview

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHead_TitleAndOG(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head>
  <title>  My Page  </title>
  <meta property="og:image" content="https://cdn.example/cover.png">
  <meta property="og:description" content="A nice page.">
  <link rel="icon" href="/favicon.png">
</head>
<body>...</body>
</html>`
	got := parseHead(strings.NewReader(html))
	assert.Equal(t, "My Page", got.Title)
	assert.Equal(t, "https://cdn.example/cover.png", got.OGImageURL)
	assert.Equal(t, "A nice page.", got.Description)
	assert.Equal(t, "/favicon.png", got.FaviconURL)
}

func TestParseHead_FallbackToOgTitleAndMetaDescription(t *testing.T) {
	html := `<head>
  <meta name="description" content="meta-desc">
  <meta property="og:title" content="OG Title">
</head>`
	got := parseHead(strings.NewReader(html))
	assert.Equal(t, "OG Title", got.Title)
	assert.Equal(t, "meta-desc", got.Description)
}

func TestParseHead_ShortcutIconRel(t *testing.T) {
	html := `<head>
  <link rel="shortcut icon" href="/sicon.ico">
</head>`
	got := parseHead(strings.NewReader(html))
	assert.Equal(t, "/sicon.ico", got.FaviconURL)
}

func TestResolveRelatives(t *testing.T) {
	base, _ := url.Parse("https://example.com/path/page.html")
	in := Result{
		FaviconURL: "/favicon.png",
		OGImageURL: "img/og.jpg",
	}
	got := resolveRelatives(in, base)
	assert.Equal(t, "https://example.com/favicon.png", got.FaviconURL)
	assert.Equal(t, "https://example.com/path/img/og.jpg", got.OGImageURL)
}

func TestResolveOne(t *testing.T) {
	base, _ := url.Parse("https://example.com/a/b")
	assert.Equal(t, "", resolveOne("", base))
	assert.Equal(t, "https://cdn.example/x.png", resolveOne("https://cdn.example/x.png", base))
	assert.Equal(t, "https://example.com/abs", resolveOne("/abs", base))
	assert.Equal(t, "https://example.com/a/rel", resolveOne("rel", base))
}

func TestAttrAndIsVoid(t *testing.T) {
	require.True(t, isVoid("br"))
	require.True(t, isVoid("img"))
	require.False(t, isVoid("div"))
}

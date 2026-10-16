package preview

import (
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dripReader blocks after its scripted chunks run out: readHeadBytes must
// return at </head> without waiting for the rest of the body.
type dripReader struct {
	chunks [][]byte
	block  chan struct{}
}

func (d *dripReader) Read(p []byte) (int, error) {
	if len(d.chunks) == 0 {
		<-d.block
		return 0, io.EOF
	}
	n := copy(p, d.chunks[0])
	d.chunks = d.chunks[1:]
	return n, nil
}

func TestReadHeadBytes_StopsAtHeadEnd(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	r := &dripReader{chunks: [][]byte{
		[]byte("<html><head><title>x</title></he"),
		[]byte("ad><body>never read"),
	}, block: block}
	done := make(chan []byte, 1)
	go func() { done <- readHeadBytes(r) }()
	select {
	case got := <-done:
		assert.Contains(t, string(got), "</head>")
	case <-time.After(2 * time.Second):
		t.Fatal("readHeadBytes kept reading past </head>")
	}
}

func TestReadHeadBytes_CapsAtTwoMiB(t *testing.T) {
	big := strings.NewReader(strings.Repeat("a", 3<<20))
	got := readHeadBytes(big)
	assert.Len(t, got, 2<<20)
}

func TestParseHead_EmptyTitleDoesNotDropFollowingOGImage(t *testing.T) {
	for _, raw := range []string{
		`<title></title><meta property="og:image" content="https://x/y.jpg">`,
		`<html><title></title><meta property="og:image" content="https://x/y.jpg">`,
	} {
		got := parseHead(strings.NewReader(raw))
		assert.Equal(t, "https://x/y.jpg", got.OGImageURL, raw)
	}
}

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

func TestResolveRelatives_HTTPImageIsStoredAsHTTPS(t *testing.T) {
	base, err := url.Parse("https://foldex.example/")
	require.NoError(t, err)
	got := resolveRelatives(Result{
		OGImageURL: "http://www.dropitbrand.com/cdn/shop/files/seo-image.png?v=1749688874",
		FaviconURL: "http://cdn.example/favicon.ico",
		OEmbedURL:  "http://cdn.example/oembed",
	}, base)
	assert.Equal(t, "https://www.dropitbrand.com/cdn/shop/files/seo-image.png?v=1749688874", got.OGImageURL)
	assert.Equal(t, "https://cdn.example/favicon.ico", got.FaviconURL)
	assert.Equal(t, "http://cdn.example/oembed", got.OEmbedURL,
		"oEmbed is fetched server-side; mixed-content rewrite does not apply")
}

func TestResolveRelatives_RelativeAgainstHTTPBaseBecomesHTTPS(t *testing.T) {
	base, err := url.Parse("http://example.com/path/page.html")
	require.NoError(t, err)
	got := resolveRelatives(Result{OGImageURL: "/cover.png"}, base)
	assert.Equal(t, "https://example.com/cover.png", got.OGImageURL)
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

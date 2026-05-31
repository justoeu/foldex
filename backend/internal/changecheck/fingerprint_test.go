package changecheck

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeGetter struct {
	body []byte
	err  error
}

func (f fakeGetter) GetRaw(_ context.Context, _ string) ([]byte, string, error) {
	return f.body, "application/rss+xml", f.err
}

func TestExtractFeedURL_Found(t *testing.T) {
	htmlBody := `<html><head>
        <link rel="icon" href="/favicon.ico">
        <link rel="alternate" type="application/rss+xml" href="/feed.xml">
    </head><body></body></html>`
	got := extractFeedURL(htmlBody, "https://example.test/blog")
	assert.Equal(t, "https://example.test/feed.xml", got)
}

func TestExtractFeedURL_AtomAccepted(t *testing.T) {
	htmlBody := `<html><head>
        <link rel="alternate" type="application/atom+xml" href="https://example.test/atom">
    </head></html>`
	got := extractFeedURL(htmlBody, "https://example.test/")
	assert.Equal(t, "https://example.test/atom", got)
}

func TestExtractFeedURL_NoFeedDeclared(t *testing.T) {
	htmlBody := `<html><head><title>X</title></head><body>nothing</body></html>`
	assert.Equal(t, "", extractFeedURL(htmlBody, "https://x.test/"))
}

func TestExtractFeedURL_WrongTypeIgnored(t *testing.T) {
	htmlBody := `<html><head>
        <link rel="alternate" type="text/html" href="/other-lang.html">
    </head></html>`
	assert.Equal(t, "", extractFeedURL(htmlBody, "https://x.test/"))
}

func TestNormalizeWhitespace_Collapses(t *testing.T) {
	got := normalizeWhitespace("   hello\n\n\nworld\t  ")
	assert.Equal(t, "hello world", got)
}

func TestExtractMainContent_PrefersMain(t *testing.T) {
	got := extractMainContent(`<html><body>
        <header>header text</header>
        <nav>nav text</nav>
        <main>main content here</main>
        <article>article body</article>
        <footer>footer</footer>
    </body></html>`)
	assert.Contains(t, got, "main content here")
	assert.NotContains(t, got, "header text")
	assert.NotContains(t, got, "nav text")
	assert.NotContains(t, got, "footer")
}

func TestExtractMainContent_FallsBackToArticle(t *testing.T) {
	got := extractMainContent(`<html><body>
        <header>x</header>
        <article>article body</article>
        <footer>y</footer>
    </body></html>`)
	assert.Contains(t, got, "article body")
}

func TestExtractMainContent_FallsBackToBody(t *testing.T) {
	got := extractMainContent(`<html><body>
        <p>just a body</p>
        <script>noise=1</script>
    </body></html>`)
	assert.Contains(t, got, "just a body")
	assert.NotContains(t, got, "noise=1")
}

func TestFingerprintContent_StableUnderWhitespace(t *testing.T) {
	a, errA := fingerprintContent(`<html><body><main>hello world</main></body></html>`)
	require.NoError(t, errA)
	b, errB := fingerprintContent("<html>\n\n<body><main>hello\n\n  world</main></body></html>")
	require.NoError(t, errB)
	assert.Equal(t, a, b, "whitespace differences must not change the content hash")
}

func TestFingerprintContent_DifferentWhenContentDiffers(t *testing.T) {
	a, _ := fingerprintContent(`<html><body><main>hello world</main></body></html>`)
	b, _ := fingerprintContent(`<html><body><main>hello mars</main></body></html>`)
	assert.NotEqual(t, a, b)
}

func TestFingerprintContent_IgnoresScriptAndStyle(t *testing.T) {
	a, _ := fingerprintContent(`<html><body><main>hello<script>analytics(1)</script></main></body></html>`)
	b, _ := fingerprintContent(`<html><body><main>hello<script>analytics(99)</script></main></body></html>`)
	assert.Equal(t, a, b, "script content must not influence the fingerprint")
}

func TestExtractFeedItemIDs_OrderIndependent(t *testing.T) {
	feedA := `<?xml version="1.0"?><rss><channel>
        <item><guid>a</guid></item>
        <item><guid>b</guid></item>
        <item><guid>c</guid></item>
    </channel></rss>`
	feedB := `<?xml version="1.0"?><rss><channel>
        <item><guid>c</guid></item>
        <item><guid>a</guid></item>
        <item><guid>b</guid></item>
    </channel></rss>`
	idsA := extractFeedItemIDs(feedA)
	idsB := extractFeedItemIDs(feedB)
	assert.ElementsMatch(t, idsA, idsB)
}

func TestExtractFeedItemIDs_DetectsNewItem(t *testing.T) {
	feedA := `<rss><channel><item><guid>a</guid></item></channel></rss>`
	feedB := `<rss><channel>
        <item><guid>a</guid></item>
        <item><guid>b</guid></item>
    </channel></rss>`
	idsA := extractFeedItemIDs(feedA)
	idsB := extractFeedItemIDs(feedB)
	assert.NotEqual(t, len(idsA), len(idsB))
}

func TestExtractFeedItemIDs_AtomEntryAndID(t *testing.T) {
	feed := `<feed xmlns="http://www.w3.org/2005/Atom">
        <entry><id>urn:1</id></entry>
        <entry><id>urn:2</id></entry>
    </feed>`
	ids := extractFeedItemIDs(feed)
	assert.ElementsMatch(t, []string{"urn:1", "urn:2"}, ids)
}

func TestCompute_FeedKindWhenDeclared(t *testing.T) {
	feed := `<rss><channel>
        <item><guid>id-1</guid></item>
        <item><guid>id-2</guid></item>
    </channel></rss>`
	page := `<html><head>
        <link rel="alternate" type="application/rss+xml" href="https://x.test/feed.xml">
    </head><body>fallback content</body></html>`

	fp := NewFingerprinter(fakeGetter{body: []byte(feed)})
	kind, hash, err := fp.Compute(context.Background(), "https://x.test/", page)
	require.NoError(t, err)
	assert.Equal(t, KindFeed, kind)
	assert.NotEmpty(t, hash)
}

func TestCompute_FallsBackToContentWhenFeedFails(t *testing.T) {
	page := `<html><head>
        <link rel="alternate" type="application/rss+xml" href="https://x.test/feed.xml">
    </head><body><main>plain content</main></body></html>`

	fp := NewFingerprinter(fakeGetter{err: errors.New("network down")})
	kind, hash, err := fp.Compute(context.Background(), "https://x.test/", page)
	require.NoError(t, err)
	assert.Equal(t, KindContent, kind)
	assert.NotEmpty(t, hash)
}

func TestCompute_ContentKindWhenNoFeed(t *testing.T) {
	page := `<html><body><main>just a page</main></body></html>`
	fp := NewFingerprinter(fakeGetter{})
	kind, _, err := fp.Compute(context.Background(), "https://x.test/", page)
	require.NoError(t, err)
	assert.Equal(t, KindContent, kind)
}

func TestSplitFingerprint(t *testing.T) {
	k, h := SplitFingerprint("feed:abcdef")
	assert.Equal(t, "feed", k)
	assert.Equal(t, "abcdef", h)

	k, h = SplitFingerprint("")
	assert.Equal(t, "", k)
	assert.Equal(t, "", h)
}

func TestFormatFingerprint(t *testing.T) {
	got := FormatFingerprint(KindContent, "abc")
	assert.Equal(t, "content:abc", got)
}

func TestFingerprintContent_EmptyBodyErrors(t *testing.T) {
	_, err := fingerprintContent(`<html><body></body></html>`)
	assert.Error(t, err)
}

// Sanity check: the fingerprint really is a sha256 hex string (64 chars).
func TestFingerprintContent_HexLength(t *testing.T) {
	h, err := fingerprintContent(`<html><body><main>hello</main></body></html>`)
	require.NoError(t, err)
	assert.Len(t, h, 64)
	assert.True(t, strings.IndexFunc(h, func(r rune) bool {
		return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
	}) == -1, "fingerprint must be lowercase hex only")
}

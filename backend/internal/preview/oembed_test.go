package preview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHardcodedOEmbedURL(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string // expected host in the oEmbed endpoint, "" if no shortcut
	}{
		{"https://www.youtube.com/watch?v=abc", "www.youtube.com"},
		{"https://youtu.be/abc", "www.youtube.com"},
		{"https://m.youtube.com/watch?v=abc", "www.youtube.com"},
		{"https://music.youtube.com/watch?v=abc", "www.youtube.com"},
		{"https://vimeo.com/12345", "vimeo.com"},
		{"https://www.vimeo.com/12345", "vimeo.com"},
		{"https://YOUTUBE.com/watch?v=abc", "www.youtube.com"}, // case-insensitive host
		{"https://example.com/page", ""},                       // unknown host — no shortcut
		{"https://twitter.com/user/status/1", ""},              // not in hardcoded list (caught by discovery)
		{"not-a-url", ""}, // parse failure → empty
	}
	for _, c := range cases {
		got := hardcodedOEmbedURL(c.in)
		if c.wantHost == "" {
			assert.Equal(t, "", got, "input=%q expected empty", c.in)
			continue
		}
		require.NotEmpty(t, got, "input=%q expected non-empty oEmbed URL", c.in)
		u, err := url.Parse(got)
		require.NoError(t, err)
		assert.Equal(t, c.wantHost, u.Host, "input=%q", c.in)
		// The page URL must round-trip in the `url` query param.
		assert.Equal(t, c.in, u.Query().Get("url"), "input=%q", c.in)
	}
}

// startOEmbedServer spins up an httptest server that mimics an oEmbed provider.
// Returns the server (caller closes) and the URL template caller would have
// stored in `knownOEmbedProviders` (with a %s slot for the page URL).
func startOEmbedServer(t *testing.T, resp oembedResponse) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return srv
}

func TestFetcher_FetchOEmbed_HappyPath(t *testing.T) {
	srv := startOEmbedServer(t, oembedResponse{
		Title:        "  Gitea Actions Setup  ",
		Description:  "How to set up Gitea Actions",
		ThumbnailURL: "https://i.ytimg.com/vi/X/maxresdefault.jpg",
	})
	defer srv.Close()

	// Need strict SSRF off for the httptest loopback server.
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)
	r, err := f.fetchOEmbed(context.Background(), srv.URL+"/oembed")
	require.NoError(t, err)
	assert.Equal(t, "Gitea Actions Setup", r.Title, "title must be TrimSpace'd")
	assert.Equal(t, "How to set up Gitea Actions", r.Description)
	assert.Equal(t, "https://i.ytimg.com/vi/X/maxresdefault.jpg", r.OGImageURL)
	assert.Equal(t, "", r.FaviconURL, "oEmbed never provides favicon")
}

func TestFetcher_FetchOEmbed_NonJSONIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)
	_, err := f.fetchOEmbed(context.Background(), srv.URL)
	require.Error(t, err)
}

func TestFetcher_FetchOEmbed_5xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)
	_, err := f.fetchOEmbed(context.Background(), srv.URL)
	require.Error(t, err)
}

func TestParseHead_CapturesOEmbedDiscoveryLink(t *testing.T) {
	const body = `<html><head>
		<title>X</title>
		<link rel="alternate" type="application/json+oembed" href="https://example.com/oembed?url=foo">
		<link rel="alternate" type="application/xml+oembed" href="https://example.com/oembed.xml">
	</head><body></body></html>`
	r := parseHead(strings.NewReader(body))
	assert.Equal(t, "X", r.Title)
	assert.Equal(t, "https://example.com/oembed?url=foo", r.OEmbedURL,
		"only the JSON oEmbed link must be captured (XML variant is unused)")
}

func TestParseHead_NoOEmbedLinkLeavesFieldEmpty(t *testing.T) {
	const body = `<html><head><title>X</title></head><body></body></html>`
	r := parseHead(strings.NewReader(body))
	assert.Equal(t, "X", r.Title)
	assert.Equal(t, "", r.OEmbedURL)
}

func TestMergeOEmbed_NeverOverwritesHTML(t *testing.T) {
	// The merge contract is "fill only the gaps". HTML always wins what it
	// has; oEmbed only backfills empty fields.
	html := Result{Title: "HTML Title", Description: "HTML Desc", OGImageURL: "HTML Image"}
	oe := Result{Title: "oE Title", Description: "oE Desc", OGImageURL: "oE Image"}
	got := mergeOEmbed(html, oe)
	assert.Equal(t, "HTML Title", got.Title)
	assert.Equal(t, "HTML Desc", got.Description)
	assert.Equal(t, "HTML Image", got.OGImageURL)
}

func TestMergeOEmbed_FillsEmptyHTMLFields(t *testing.T) {
	html := Result{Title: "", Description: "HTML Desc", OGImageURL: ""}
	oe := Result{Title: "oE Title", Description: "oE Desc", OGImageURL: "oE Image"}
	got := mergeOEmbed(html, oe)
	assert.Equal(t, "oE Title", got.Title, "empty HTML title must be backfilled")
	assert.Equal(t, "HTML Desc", got.Description, "non-empty HTML desc must survive")
	assert.Equal(t, "oE Image", got.OGImageURL, "empty HTML image must be backfilled")
}

func TestFetcher_Fetch_HardcodedShortCircuit_NeverHitsHTML(t *testing.T) {
	// Spin up an oEmbed server we'll redirect the hardcoded YouTube template
	// to via a local proxy. Simpler approach: monkey-patch knownOEmbedProviders
	// inside the test so the hardcoded shortcut points at our stub.
	srv := startOEmbedServer(t, oembedResponse{
		Title:        "Real Video Title",
		ThumbnailURL: "https://example.com/thumb.jpg",
	})
	defer srv.Close()

	// Save + restore the production registry around the test.
	const fakeHost = "fake.test"
	knownOEmbedProviders[fakeHost] = srv.URL + "?url=%s"
	defer delete(knownOEmbedProviders, fakeHost)

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)

	// The page URL itself doesn't have to be reachable — the shortcut
	// path never opens it. If the implementation regresses and falls
	// through to HTML fetch, this URL would fail (DNS or net error)
	// and the test would surface it.
	r, err := f.Fetch(context.Background(), "https://fake.test/page?id=1")
	require.NoError(t, err)
	assert.Equal(t, "Real Video Title", r.Title)
	assert.Equal(t, "https://example.com/thumb.jpg", r.OGImageURL)
	assert.Equal(t, "https://fake.test/favicon.ico", r.FaviconURL,
		"FaviconURL is synthesized from the page host when oEmbed shortcut wins")
}

func TestFetcher_Fetch_DiscoveryEnrichmentFillsGaps(t *testing.T) {
	// Stage 1: oEmbed server that supplies title + image.
	oe := startOEmbedServer(t, oembedResponse{
		Title:        "Enriched Title",
		ThumbnailURL: "https://example.com/thumb.jpg",
	})
	defer oe.Close()

	// Stage 2: page server that returns HTML with description only +
	// the oEmbed alternate link pointing at the stage-1 server.
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
			<meta property="og:description" content="Page desc">
			<link rel="alternate" type="application/json+oembed" href="` + oe.URL + `">
		</head><body></body></html>`))
	}))
	defer page.Close()

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)

	r, err := f.Fetch(context.Background(), page.URL+"/article")
	require.NoError(t, err)
	assert.Equal(t, "Enriched Title", r.Title, "empty HTML title backfilled from oEmbed")
	assert.Equal(t, "Page desc", r.Description, "HTML description survives — oEmbed never overwrites")
	assert.Equal(t, "https://example.com/thumb.jpg", r.OGImageURL, "empty HTML image backfilled from oEmbed")
	assert.Equal(t, "", r.OEmbedURL, "internal field must be cleared before returning")
}

func TestFetcher_FetchOEmbed_RejectsNonHTTPScheme(t *testing.T) {
	// HIGH security finding from the v1.4 sweep: an oEmbed `href` captured
	// from arbitrary remote HTML can advertise file:///, gopher://,
	// unix:// etc. Those bypass the IP-level SSRF dialer because they
	// never hit a real socket dial. fetchOEmbed must refuse at the edge.
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)
	cases := []string{
		"file:///etc/passwd",
		"gopher://example.com/",
		"ftp://example.com/",
		"javascript:alert(1)",
		"data:text/json,{}",
		"unix:///var/run/foo.sock",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := f.fetchOEmbed(context.Background(), in)
			require.Error(t, err, "fetchOEmbed must refuse %q", in)
			assert.Contains(t, err.Error(), "invalid url scheme",
				"error must be the scheme guard, not a generic transport error")
		})
	}
}

func TestFetcher_Fetch_HardcodedShortCircuit_FallsThroughOnEmptyTitle(t *testing.T) {
	// Lock the contract from fetcher.go: when the oEmbed shortcut returns
	// successfully but with a whitespace-only or empty title, we MUST fall
	// through to HTML parsing rather than ship the empty title back to the
	// user. A regression that drops the TrimSpace check would surface here.
	oe := startOEmbedServer(t, oembedResponse{Title: "   ", ThumbnailURL: ""}) // whitespace title
	defer oe.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Real HTML Title</title></head></html>`))
	}))
	defer page.Close()

	pageURL, err := url.Parse(page.URL)
	require.NoError(t, err)
	knownOEmbedProviders[pageURL.Host] = oe.URL + "?url=%s"
	defer delete(knownOEmbedProviders, pageURL.Host)

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)
	r, err := f.Fetch(context.Background(), page.URL+"/page")
	require.NoError(t, err)
	assert.Equal(t, "Real HTML Title", r.Title,
		"empty oEmbed title must fall through to HTML — TrimSpace guard locks this")
}

func TestFetcher_Fetch_HardcodedShortCircuit_FallsThroughOnError(t *testing.T) {
	// Symmetric to the empty-title case: when oEmbed returns 5xx (provider
	// outage), Fetch must NOT bail — it should run the HTML path. The
	// shortcut is an optimization, not a hard dependency.
	oe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer oe.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Fallback HTML</title></head></html>`))
	}))
	defer page.Close()

	pageURL, err := url.Parse(page.URL)
	require.NoError(t, err)
	knownOEmbedProviders[pageURL.Host] = oe.URL + "?url=%s"
	defer delete(knownOEmbedProviders, pageURL.Host)

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)
	r, err := f.Fetch(context.Background(), page.URL+"/page")
	require.NoError(t, err)
	assert.Equal(t, "Fallback HTML", r.Title)
}

func TestFetcher_Fetch_DiscoveryURL_ResolvedAgainstPageBase(t *testing.T) {
	// REAL BUG from the v1.4 sweep: pages whose oEmbed discovery link is
	// path-relative (`<link rel=alternate href="/oembed?url=…">`) — common
	// in WordPress, Flickr, SoundCloud — would have failed the second
	// fetch with `invalid url` before the resolveRelatives fix. This test
	// locks the fix.
	oe := startOEmbedServer(t, oembedResponse{Title: "From Relative oEmbed"})
	defer oe.Close()

	// Strip scheme+host off the oEmbed URL to simulate a path-relative href.
	oeAbs, err := url.Parse(oe.URL)
	require.NoError(t, err)
	relativeHref := oeAbs.Path + "?url=foo"
	if relativeHref == "?url=foo" {
		relativeHref = "/?url=foo"
	}

	// The page server hands out HTML where the oEmbed alternate link is
	// relative AND serves the oEmbed JSON on the same host's root path.
	var page *httptest.Server
	page = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/article" {
			// Proxy to the oe server for the relative oEmbed path.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(oembedResponse{Title: "From Relative oEmbed"})
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
			<link rel="alternate" type="application/json+oembed" href="` + relativeHref + `">
		</head><body></body></html>`))
	}))
	defer page.Close()

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)
	r, err := f.Fetch(context.Background(), page.URL+"/article")
	require.NoError(t, err)
	assert.Equal(t, "From Relative oEmbed", r.Title,
		"relative oEmbed href must be resolved against the page URL before the second fetch")
}

func TestFetcher_FetchOEmbed_BodyCapTruncatesGracefully(t *testing.T) {
	// 64 KiB cap on the oEmbed response. A hostile provider returning a
	// multi-MB JSON must NOT pin our memory — io.LimitReader truncates and
	// the truncated prefix is parsed (succeeds if valid prefix is JSON,
	// errors cleanly otherwise). Either outcome is OK as long as we don't
	// allocate the full body.
	huge := strings.Repeat("x", 200<<10) // 200 KiB of payload + JSON envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Title is valid JSON but the document is way over the cap.
		_, _ = w.Write([]byte(`{"title":"head","description":"` + huge + `","thumbnail_url":""}`))
	}))
	defer srv.Close()

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)
	_, err := f.fetchOEmbed(context.Background(), srv.URL)
	// JSON parse will error because the body is truncated mid-string.
	// That's fine: caller treats it as "no oEmbed" and falls back to HTML.
	require.Error(t, err, "truncated JSON must surface as a parse error, not a panic")
}

func TestFetcher_Fetch_DiscoveryEnrichment_HTMLImageWinsOverOEmbed(t *testing.T) {
	// End-to-end version of TestMergeOEmbed_NeverOverwritesHTML — through
	// the real Fetch path. HTML has og:image, oEmbed has a different
	// thumbnail; the HTML one must survive the merge.
	oe := startOEmbedServer(t, oembedResponse{
		Title:        "oE Title",
		ThumbnailURL: "https://oe.example/wrong.jpg",
	})
	defer oe.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
			<meta property="og:image" content="https://html.example/right.jpg">
			<link rel="alternate" type="application/json+oembed" href="` + oe.URL + `">
		</head><body></body></html>`))
	}))
	defer page.Close()

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)
	r, err := f.Fetch(context.Background(), page.URL+"/article")
	require.NoError(t, err)
	assert.Equal(t, "https://html.example/right.jpg", r.OGImageURL,
		"HTML og:image must beat oEmbed thumbnail in merge")
	assert.Equal(t, "oE Title", r.Title,
		"empty HTML title is still backfilled from oEmbed")
}

func TestFetcher_Fetch_DiscoverySkippedWhenAllFieldsFilled(t *testing.T) {
	// If the HTML pass already has Title + Description + OGImageURL, the
	// discovery enrichment must NOT fire — saves a redundant network call.
	// We assert this by failing the test if the oEmbed server is hit.
	oeHits := 0
	oe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		oeHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"oE","thumbnail_url":"oE"}`))
	}))
	defer oe.Close()

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
			<title>Full Title</title>
			<meta property="og:description" content="Full desc">
			<meta property="og:image" content="https://example.com/i.jpg">
			<link rel="alternate" type="application/json+oembed" href="` + oe.URL + `">
		</head></html>`))
	}))
	defer page.Close()

	t.Setenv("PREVIEW_STRICT_SSRF", "")
	f := NewFetcher(5 * time.Second)

	r, err := f.Fetch(context.Background(), page.URL+"/")
	require.NoError(t, err)
	assert.Equal(t, "Full Title", r.Title)
	assert.Equal(t, "Full desc", r.Description)
	assert.Equal(t, 0, oeHits, "oEmbed must NOT be called when all fields are already filled")
}

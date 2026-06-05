package links

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubMetadataFetcher lets the handler tests exercise the JSON contract
// without spinning up an httptest origin server for every case. The fetch
// path itself (HTML parsing + SSRF guard) is covered by the preview package's
// own tests — here we only validate the handler's edge handling and the
// pass-through shape.
type stubMetadataFetcher struct {
	out URLMetadata
	err error
	// lastURL captures the most recent argument so request-shaping tests can
	// assert the handler trimmed/normalized the input before calling.
	lastURL string
}

func (s *stubMetadataFetcher) FetchMetadata(_ context.Context, pageURL string) (URLMetadata, error) {
	s.lastURL = pageURL
	if s.err != nil {
		return URLMetadata{}, s.err
	}
	return s.out, nil
}

// newMetadataRouter wires just the bits this file needs from links.Handler —
// no repo, no worker, no DB. The route lives on a Chi sub-router so the
// production mount path (/api/links/url-metadata stripping to /url-metadata)
// is the same shape the handler sees here.
func newMetadataRouter(f MetadataFetcher) http.Handler {
	r := chi.NewRouter()
	h := &Handler{fetcher: f}
	r.Get("/url-metadata", h.fetchURLMetadata)
	return r
}

func doGet(t *testing.T, h http.Handler, rawURL string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/url-metadata?url="+url.QueryEscape(rawURL), nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	resp := rr.Result()
	body := rr.Body.Bytes()
	return resp, body
}

func TestFetchURLMetadata_Success(t *testing.T) {
	stub := &stubMetadataFetcher{
		out: URLMetadata{
			Title:       "Hacker News",
			Description: "Tech news",
			FaviconURL:  "https://news.ycombinator.com/favicon.ico",
			OGImageURL:  "https://news.ycombinator.com/y18.svg",
		},
	}
	resp, body := doGet(t, newMetadataRouter(stub), "https://news.ycombinator.com")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "https://news.ycombinator.com", stub.lastURL)

	var got URLMetadata
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "Hacker News", got.Title)
	assert.Equal(t, "Tech news", got.Description)
	assert.Equal(t, "https://news.ycombinator.com/favicon.ico", got.FaviconURL)
	assert.Equal(t, "https://news.ycombinator.com/y18.svg", got.OGImageURL)
}

func TestFetchURLMetadata_TrimsWhitespace(t *testing.T) {
	stub := &stubMetadataFetcher{out: URLMetadata{Title: "ok"}}
	resp, _ := doGet(t, newMetadataRouter(stub), "  https://example.com  ")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "https://example.com", stub.lastURL)
}

func TestFetchURLMetadata_MissingURL(t *testing.T) {
	stub := &stubMetadataFetcher{}
	req := httptest.NewRequest(http.MethodGet, "/url-metadata", nil)
	rr := httptest.NewRecorder()
	newMetadataRouter(stub).ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_url")
	assert.Empty(t, stub.lastURL, "fetcher must not be called when url is missing")
}

func TestFetchURLMetadata_EmptyURL(t *testing.T) {
	stub := &stubMetadataFetcher{}
	resp, body := doGet(t, newMetadataRouter(stub), "")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), "invalid_url")
	assert.Empty(t, stub.lastURL)
}

func TestFetchURLMetadata_RejectsLongURL(t *testing.T) {
	stub := &stubMetadataFetcher{}
	long := "https://example.com/" + strings.Repeat("a", urlMetadataMaxLen)
	resp, body := doGet(t, newMetadataRouter(stub), long)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), "invalid_url")
	assert.Empty(t, stub.lastURL, "fetcher must not be called when url exceeds the cap")
}

func TestFetchURLMetadata_RejectsNonHTTPScheme(t *testing.T) {
	stub := &stubMetadataFetcher{}
	cases := []string{
		"javascript:alert(1)",
		"mailto:foo@bar.com",
		"file:///etc/passwd",
		"ftp://example.com",
		"data:text/html,<h1>x",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			stub.lastURL = ""
			resp, body := doGet(t, newMetadataRouter(stub), raw)
			require.Equal(t, http.StatusBadRequest, resp.StatusCode, "raw=%s", raw)
			s := string(body)
			assert.True(t,
				strings.Contains(s, "invalid_scheme") || strings.Contains(s, "invalid_url"),
				"unexpected body for %s: %s", raw, s,
			)
			assert.Empty(t, stub.lastURL, "fetcher must not be called for bad scheme")
		})
	}
}

func TestFetchURLMetadata_FetcherErrorMaskedAs502(t *testing.T) {
	// The fetcher can fail for many reasons (DNS, SSRF refusal, TLS, 4xx
	// from origin). The handler must NOT leak those details to the client —
	// every failure mode collapses to a uniform 502 fetch_failed envelope.
	stub := &stubMetadataFetcher{err: errors.New("ssrf: refusing IMDS endpoint 169.254.169.254")}
	resp, body := doGet(t, newMetadataRouter(stub), "https://attacker.example/")
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	s := string(body)
	assert.Contains(t, s, "fetch_failed")
	assert.NotContains(t, s, "ssrf", "internal error text must not reach the client")
	assert.NotContains(t, s, "IMDS")
	assert.NotContains(t, s, "169.254")
}

func TestFetchURLMetadata_NoFetcherWired(t *testing.T) {
	// If router boots without a fetcher (e.g. test harness), the route exists
	// but responds 503 instead of dereferencing nil and 500-ing.
	resp, body := doGet(t, newMetadataRouter(nil), "https://example.com")
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, string(body), "metadata_unconfigured")
}

func TestFetchURLMetadata_TruncatesOversizedFields(t *testing.T) {
	// A hostile page can return arbitrarily large <title>/<meta content>/
	// <link href> within the fetcher's 2 MiB body cap. The handler must
	// truncate each field at the documented byte caps so the response
	// never balloons proportionally to attacker input — UI text fields
	// (description capped at 1000 chars by keystroke handler) would
	// otherwise be bypassed by programmatic setDescription().
	stub := &stubMetadataFetcher{
		out: URLMetadata{
			Title:       strings.Repeat("A", MaxTitleBytes*2),
			Description: strings.Repeat("B", descByteCap*2),
			FaviconURL:  "https://x/" + strings.Repeat("c", urlFieldByteCap*2),
			OGImageURL:  "https://x/" + strings.Repeat("d", urlFieldByteCap*2),
		},
	}
	resp, body := doGet(t, newMetadataRouter(stub), "https://news.example/")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got URLMetadata
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, MaxTitleBytes, len(got.Title), "title must be capped to MaxTitleBytes (DTO limit)")
	assert.Equal(t, descByteCap, len(got.Description), "description must be capped")
	assert.Equal(t, urlFieldByteCap, len(got.FaviconURL), "favicon_url must be capped")
	assert.Equal(t, urlFieldByteCap, len(got.OGImageURL), "og_image_url must be capped")
}

func TestFetchURLMetadata_TitleCapMatchesDTOContract(t *testing.T) {
	// Lock the alignment between metadata pre-fill and the Create/Update DTO:
	// a title at exactly MaxTitleBytes must round-trip through Validate(). If
	// either side drifts (DTO bumps to 1000, metadata caps at 500), a
	// pre-filled title would be silently truncated AND then rejected on Save.
	in := CreateInput{URL: "https://x.test", Title: strings.Repeat("a", MaxTitleBytes)}
	require.NoError(t, in.Validate(), "title at exactly MaxTitleBytes must pass DTO Validate")
	in.Title = strings.Repeat("a", MaxTitleBytes+1)
	require.Error(t, in.Validate(), "title one over MaxTitleBytes must fail DTO Validate")
}

func TestTruncateRunes_RespectsUTF8Boundary(t *testing.T) {
	// '€' encodes as 3 bytes (0xE2 0x82 0xAC). If we naively slice at byte
	// 7 (mid-rune), the result would contain a half-rune that breaks JSON
	// encoding and downstream readers. truncateRunes walks back to the
	// nearest rune boundary so the output is always valid UTF-8.
	in := "abc€€€" // 3 + (3*3) = 12 bytes
	got := truncateRunes(in, 7)
	// We expect "abc€" (3 + 3 = 6 bytes), because byte 7 lands mid '€'
	// and we walk back to byte 6 which is the boundary.
	assert.Equal(t, "abc€", got)
	assert.True(t, len(got) <= 7)
}

func TestFetchURLMetadata_EmptyFieldsRoundTrip(t *testing.T) {
	// A page with no og:* / <title> / <link rel=icon> yields zero-value
	// fields. The handler must still 200 with all four keys present so the
	// frontend's "fill if empty" check has stable shape to read from.
	stub := &stubMetadataFetcher{out: URLMetadata{}}
	resp, body := doGet(t, newMetadataRouter(stub), "https://blank.example/")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	for _, k := range []string{"title", "description", "favicon_url", "og_image_url"} {
		v, ok := raw[k]
		assert.True(t, ok, "field %q missing from response", k)
		assert.Equal(t, "", v, "field %q should serialize as empty string, got %v", k, v)
	}
}

package links

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authctx/authctxtest"
	"foldex/internal/pkg/authgate"
	"foldex/internal/roleperm"
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
	calls   atomic.Int32
}

func (s *stubMetadataFetcher) FetchMetadata(_ context.Context, pageURL string) (URLMetadata, error) {
	s.calls.Add(1)
	s.lastURL = pageURL
	if s.err != nil {
		return URLMetadata{}, s.err
	}
	return s.out, nil
}

// newMetadataRouter wires just the bits this file needs from links.Handler —
// no repo, no worker, no DB. Mount is the production registration so a GET
// leftover or a POST that skipped the write gate fails here, not in prod.
func newMetadataRouter(f MetadataFetcher) http.Handler {
	return newGatedMetadataRouter(f, authctx.Principal{
		UserID: authctxtest.DefaultUser,
		Role:   authctx.RoleAdmin,
		Via:    authctx.ViaSession,
	})
}

func newGatedMetadataRouter(f MetadataFetcher, p authctx.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithPrincipal(req.Context(), p)))
		})
	})
	r.Use(authgate.RequireWrite(roleperm.Default(), authctx.PermContentWrite))
	h := &Handler{fetcher: f}
	h.Mount(r)
	return r
}

func doPost(t *testing.T, h http.Handler, rawURL string) (*http.Response, []byte) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"url": rawURL})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/url-metadata", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
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
	resp, body := doPost(t, newMetadataRouter(stub), "https://news.ycombinator.com")
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
	resp, _ := doPost(t, newMetadataRouter(stub), "  https://example.com  ")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "https://example.com", stub.lastURL)
}

func TestFetchURLMetadata_MissingURL(t *testing.T) {
	stub := &stubMetadataFetcher{}
	req := httptest.NewRequest(http.MethodPost, "/url-metadata", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	newMetadataRouter(stub).ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_url")
	assert.Empty(t, stub.lastURL, "fetcher must not be called when url is missing")
}

func TestFetchURLMetadata_EmptyURL(t *testing.T) {
	stub := &stubMetadataFetcher{}
	resp, body := doPost(t, newMetadataRouter(stub), "")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), "invalid_url")
	assert.Empty(t, stub.lastURL)
}

func TestFetchURLMetadata_RejectsLongURL(t *testing.T) {
	stub := &stubMetadataFetcher{}
	long := "https://example.com/" + strings.Repeat("a", urlMetadataMaxLen)
	resp, body := doPost(t, newMetadataRouter(stub), long)
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
			resp, body := doPost(t, newMetadataRouter(stub), raw)
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

func TestFetchURLMetadata_FetcherErrorReturnsEmpty200(t *testing.T) {
	// The fetcher can fail for many reasons (DNS, SSRF refusal, TLS, 4xx
	// from origin). That is not an API fault — the dialog still Saves —
	// so the handler answers 200 with empty fields. Details must NOT leak.
	stub := &stubMetadataFetcher{err: errors.New("ssrf: refusing IMDS endpoint 169.254.169.254")}
	resp, body := doPost(t, newMetadataRouter(stub), "https://attacker.example/")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	s := string(body)
	assert.NotContains(t, s, "fetch_failed")
	assert.NotContains(t, s, "ssrf", "internal error text must not reach the client")
	assert.NotContains(t, s, "IMDS")
	assert.NotContains(t, s, "169.254")
	assert.Contains(t, s, `"title":""`)
}

func TestFetchURLMetadata_NoFetcherWired(t *testing.T) {
	// If router boots without a fetcher (e.g. test harness), the route exists
	// but responds 503 instead of dereferencing nil and 500-ing.
	resp, body := doPost(t, newMetadataRouter(nil), "https://example.com")
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
	resp, body := doPost(t, newMetadataRouter(stub), "https://news.example/")
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
	resp, body := doPost(t, newMetadataRouter(stub), "https://blank.example/")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	for _, k := range []string{"title", "description", "favicon_url", "og_image_url"} {
		v, ok := raw[k]
		assert.True(t, ok, "field %q missing from response", k)
		assert.Equal(t, "", v, "field %q should serialize as empty string, got %v", k, v)
	}
}

func TestURLMetadata_GetIsNotMounted(t *testing.T) {
	// GET used to launch Chromium without CSRF, writeGate or the API quota
	// (SameSite=Lax top-level navigation). The route is POST so those three
	// gates apply by construction.
	stub := &stubMetadataFetcher{out: URLMetadata{Title: "nope"}}
	req := httptest.NewRequest(http.MethodGet, "/url-metadata?url="+url.QueryEscape("https://example.com"), nil)
	rr := httptest.NewRecorder()
	newMetadataRouter(stub).ServeHTTP(rr, req)
	// Chi's GET /{id} catches the old path (id="url-metadata" → 400), which is
	// fine: the metadata handler must not run. 200 would be the original bug.
	require.NotEqual(t, http.StatusOK, rr.Code)
	assert.Empty(t, stub.lastURL)
	assert.Equal(t, int32(0), stub.calls.Load())
}

func TestURLMetadata_ViewerPostIsForbiddenWithoutDial(t *testing.T) {
	stub := &stubMetadataFetcher{out: URLMetadata{Title: "nope"}}
	h := newGatedMetadataRouter(stub, authctx.Principal{
		UserID: 2, Role: authctx.RoleViewer, Via: authctx.ViaSession,
	})
	resp, body := doPost(t, h, "https://example.com")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, string(body), "forbidden_role")
	assert.Empty(t, stub.lastURL)
	assert.Equal(t, int32(0), stub.calls.Load())
}

func TestURLMetadata_APITokenWithoutWriteIsForbiddenWithoutDial(t *testing.T) {
	stub := &stubMetadataFetcher{out: URLMetadata{Title: "nope"}}
	h := newGatedMetadataRouter(stub, authctx.Principal{
		UserID: 2, Role: authctx.RoleViewer, TokenID: 9, Via: authctx.ViaAPIToken,
	})
	resp, body := doPost(t, h, "https://example.com")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, string(body), "forbidden_role")
	assert.Empty(t, stub.lastURL)
	assert.Equal(t, int32(0), stub.calls.Load())
}

func TestURLMetadata_EditorAPITokenIsAllowed(t *testing.T) {
	// Content-scoped tokens inherit the account's write permission. Rejecting
	// every bearer here would break the extension; the gate is PermContentWrite.
	stub := &stubMetadataFetcher{out: URLMetadata{Title: "ok"}}
	h := newGatedMetadataRouter(stub, authctx.Principal{
		UserID: 3, Role: authctx.RoleEditor, TokenID: 4, Via: authctx.ViaAPIToken,
	})
	resp, _ := doPost(t, h, "https://example.com")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "https://example.com", stub.lastURL)
}

type blockingMetadataFetcher struct {
	entered  chan struct{}
	release  chan struct{}
	calls    atomic.Int32
	inFlight atomic.Int32
}

func (b *blockingMetadataFetcher) FetchMetadata(context.Context, string) (URLMetadata, error) {
	b.calls.Add(1)
	b.inFlight.Add(1)
	defer b.inFlight.Add(-1)
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return URLMetadata{Title: "ok"}, nil
}

func TestURLMetadata_SaturatingConcurrentPostsAreRejectedBeforeDial(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	stub := &blockingMetadataFetcher{entered: entered, release: release}
	h := newMetadataRouter(stub)

	var holdWG sync.WaitGroup
	holdWG.Add(1)
	holdCode := make(chan int, 1)
	go func() {
		defer holdWG.Done()
		resp, _ := doPost(t, h, "https://example.com/hold")
		holdCode <- resp.StatusCode
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the holder to enter FetchMetadata")
	}

	for i := 0; i < 4; i++ {
		done := make(chan int, 1)
		go func() {
			resp, body := doPost(t, h, "https://example.com/extra")
			assert.Contains(t, string(body), "metadata_busy")
			assert.Equal(t, "5", resp.Header.Get("Retry-After"))
			done <- resp.StatusCode
		}()
		select {
		case code := <-done:
			assert.Equal(t, http.StatusTooManyRequests, code)
		case <-time.After(500 * time.Millisecond):
			t.Fatal("extra POST blocked in FetchMetadata instead of 429 before dial")
		}
	}

	assert.Equal(t, int32(1), stub.calls.Load(), "rejected POSTs must not call FetchMetadata")
	assert.LessOrEqual(t, int(stub.inFlight.Load()), 1)

	releaseAll()
	holdWG.Wait()
	assert.Equal(t, http.StatusOK, <-holdCode)
}

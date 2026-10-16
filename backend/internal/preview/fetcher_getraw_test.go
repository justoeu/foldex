package preview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foldex/internal/pkg/outboundhttp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetcher_GetRaw_Success(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "0")
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = io.WriteString(w, "<rss>feed-bytes</rss>")
	}))
	defer srv.Close()

	body, contentType, err := NewFetcher(2*time.Second).GetRaw(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "<rss>feed-bytes</rss>", string(body))
	assert.Equal(t, "application/rss+xml; charset=utf-8", contentType)
	assert.Equal(t, "FoldexChangeCheckBot/1.0 (+local)", gotUA)
	assert.Contains(t, gotAccept, "application/rss+xml")
}

func TestFetcher_GetRaw_RejectsInvalidURL(t *testing.T) {
	f := NewFetcher(time.Second)
	for _, bad := range []string{"ftp://example.com/x", "/relative/path", "", "http://example.com/%zz"} {
		body, contentType, err := f.GetRaw(context.Background(), bad)
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "invalid url", bad)
		assert.Nil(t, body)
		assert.Empty(t, contentType)
	}
}

func TestFetcher_GetRaw_HTTPErrorIsTypedStatus(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer srv.Close()

	body, contentType, err := NewFetcher(time.Second).GetRaw(context.Background(), srv.URL)
	require.Error(t, err)
	assert.Nil(t, body)
	assert.Empty(t, contentType)
	var statusErr *HTTPStatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, http.StatusGone, statusErr.Code)
}

func TestFetcher_GetRaw_BlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	t.Setenv("PREVIEW_STRICT_SSRF", "1")

	_, _, err := NewFetcher(time.Second).GetRaw(context.Background(), srv.URL)
	require.ErrorIs(t, err, outboundhttp.ErrSSRF)
}

func TestFetcher_GetRaw_FollowsRedirectToFinalBody(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "0")
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusFound)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "final-body")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, contentType, err := NewFetcher(2*time.Second).GetRaw(context.Background(), srv.URL+"/old")
	require.NoError(t, err)
	assert.Equal(t, "final-body", string(body))
	assert.Equal(t, "text/plain", contentType)
}

func TestFetcher_GetRaw_TooManyRedirects(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer srv.Close()

	_, _, err := NewFetcher(2*time.Second).GetRaw(context.Background(), srv.URL+"/loop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many redirects")
}

func TestFetcher_GetRaw_CapsBodyAt4MiB(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("a", (4<<20)+4096))
	}))
	defer srv.Close()

	body, _, err := NewFetcher(5*time.Second).GetRaw(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Len(t, body, 4<<20)
}

func TestFetcher_GetRaw_UnreachableAndCancelled(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := NewFetcher(2*time.Second).GetRaw(ctx, srv.URL)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled"), "%v", err)

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	_, _, err = NewFetcher(2*time.Second).GetRaw(context.Background(), closedURL)
	require.Error(t, err)
}

func TestFetcher_GetRaw_TruncatedBodyErrors(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(rw, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 100\r\n\r\nshort")
		_ = rw.Flush()
	}))
	defer srv.Close()

	body, _, err := NewFetcher(2*time.Second).GetRaw(context.Background(), srv.URL)
	require.Error(t, err)
	assert.Nil(t, body)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

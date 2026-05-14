package preview

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the real HTTP path of the Fetcher (not just parseHead),
// using a local httptest server and the SSRF escape hatch.

func TestFetcher_Fetch_Success(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head>
            <title>OK</title>
            <meta property="og:image" content="/cover.png">
            <link rel="icon" href="/fav.ico">
        </head></html>`)
	}))
	defer srv.Close()

	f := NewFetcher(2 * time.Second)
	got, err := f.Fetch(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "OK", got.Title)
	assert.Contains(t, got.FaviconURL, "fav.ico")
	assert.Contains(t, got.OGImageURL, "cover.png")
}

func TestFetcher_Fetch_HTTPError(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	f := NewFetcher(time.Second)
	_, err := f.Fetch(context.Background(), srv.URL)
	require.Error(t, err)
}

func TestFetcher_Fetch_RejectsNonHTTP(t *testing.T) {
	f := NewFetcher(time.Second)
	_, err := f.Fetch(context.Background(), "ftp://example.com")
	require.Error(t, err)
}

func TestFetcher_Fetch_BlocksLoopbackInStrictMode(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	f := NewFetcher(time.Second)
	_, err := f.Fetch(context.Background(), srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssrf")
}

func TestFetcher_Fetch_BlocksIMDSEvenWhenPermissive(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	// IMDS is always refused — it's the one footgun without an opt-out.
	f := NewFetcher(500 * time.Millisecond)
	_, err := f.Fetch(context.Background(), "http://169.254.169.254/latest/meta-data/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IMDS")
}

func TestFetcher_Fetch_TooManyRedirects(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := NewFetcher(time.Second)
	_, err := f.Fetch(context.Background(), srv.URL)
	require.Error(t, err)
}

func TestParseHead_StopsAtBody(t *testing.T) {
	body := `<head><title>T</title></head><body><title>ignored</title></body>`
	got := parseHead(strings.NewReader(body))
	assert.Equal(t, "T", got.Title)
}

func TestParseHead_NoHeadStillExtractsTitle(t *testing.T) {
	got := parseHead(strings.NewReader(`<title>Loose</title>`))
	assert.Equal(t, "Loose", got.Title)
}

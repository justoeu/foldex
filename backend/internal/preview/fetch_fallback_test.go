package preview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRenderer struct {
	res Result
	err error
	n   int
}

func (s *stubRenderer) ExtractMetadata(context.Context, string) (Result, error) {
	s.n++
	return s.res, s.err
}

func TestFetchThenRender_HTTPTitleWinsWithoutRenderer(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>From HTTP</title></head></html>`)
	}))
	defer srv.Close()
	f := NewFetcher(2 * time.Second)
	rend := &stubRenderer{res: Result{Title: "From Chromium"}}
	got, err := f.FetchThenRender(context.Background(), srv.URL, rend)
	require.NoError(t, err)
	assert.Equal(t, "From HTTP", got.Title)
	assert.Equal(t, 0, rend.n, "renderer must not run when HTTP produced a title")
}

func TestFetchThenRender_RendererFillsAfterHTTPBlock(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bot wall", http.StatusForbidden)
	}))
	defer srv.Close()
	f := NewFetcher(2 * time.Second)
	rend := &stubRenderer{res: Result{Title: "From Chromium", Description: "Rendered"}}
	got, err := f.FetchThenRender(context.Background(), srv.URL, rend)
	require.NoError(t, err)
	assert.Equal(t, 1, rend.n)
	assert.Equal(t, "From Chromium", got.Title)
	assert.Equal(t, "Rendered", got.Description)
}

func TestFetchThenRender_BothFailReturnsHTTPError(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	f := NewFetcher(2 * time.Second)
	rend := &stubRenderer{err: errors.New("chromium blocked")}
	_, err := f.FetchThenRender(context.Background(), srv.URL, rend)
	require.Error(t, err)
	assert.Equal(t, 1, rend.n)
}

func TestFetchThenRender_EmptyHTTPTitleUsesRenderer(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><meta name="description" content="from http"></head></html>`)
	}))
	defer srv.Close()
	f := NewFetcher(2 * time.Second)
	rend := &stubRenderer{res: Result{Title: "From Chromium"}}
	got, err := f.FetchThenRender(context.Background(), srv.URL, rend)
	require.NoError(t, err)
	assert.Equal(t, 1, rend.n)
	assert.Equal(t, "From Chromium", got.Title)
	assert.Equal(t, "from http", got.Description)
}

func TestFetchThenRender_ChallengeTitleUsesRenderer(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>Just a moment...</title></head></html>`)
	}))
	defer srv.Close()
	f := NewFetcher(2 * time.Second)
	rend := &stubRenderer{res: Result{Title: "Real page"}}
	got, err := f.FetchThenRender(context.Background(), srv.URL, rend)
	require.NoError(t, err)
	assert.Equal(t, 1, rend.n)
	assert.Equal(t, "Real page", got.Title)
}

func TestFetchThenRender_SSRFDoesNotLaunchRenderer(t *testing.T) {
	f := NewFetcher(2 * time.Second)
	rend := &stubRenderer{res: Result{Title: "should not run"}}
	_, err := f.FetchThenRender(context.Background(), "http://169.254.169.254/", rend)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssrf:")
	assert.Equal(t, 0, rend.n, "Chromium must not run for an SSRF refusal")
}

func TestFetchThenRender_Origin500DoesNotLaunchRenderer(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	f := NewFetcher(2 * time.Second)
	rend := &stubRenderer{res: Result{Title: "should not run"}}
	_, err := f.FetchThenRender(context.Background(), srv.URL, rend)
	require.Error(t, err)
	assert.Equal(t, 0, rend.n)
}

func TestFetchThenRender_HTTPBudgetLeavesRoomForRenderer(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "")
	started := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		http.Error(w, "slow", http.StatusForbidden)
	}))
	defer srv.Close()
	f := NewFetcher(30 * time.Second)
	rend := &stubRenderer{res: Result{Title: "From Chromium"}}
	got, err := f.FetchThenRender(context.Background(), srv.URL, rend)
	require.NoError(t, err)
	assert.Equal(t, 1, rend.n)
	assert.Equal(t, "From Chromium", got.Title)
	assert.Less(t, time.Since(started), 8*time.Second, "HTTP pass must cap at 5s so Chromium still runs")
}

func TestUsableTitle(t *testing.T) {
	assert.False(t, usableTitle(""))
	assert.False(t, usableTitle("   "))
	assert.False(t, usableTitle("Just a moment..."))
	assert.False(t, usableTitle("Attention Required! | Cloudflare"))
	assert.True(t, usableTitle("Hacker News"))
}

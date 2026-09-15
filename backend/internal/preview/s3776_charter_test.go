package preview

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/links"
)

func TestS3776Charter_ParseHead(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		html  string
		title string
		image string
	}{
		{
			name:  "title and og:image",
			html:  `<html><head><title>OK</title><meta property="og:image" content="/cover.png"></head></html>`,
			title: "OK",
			image: "/cover.png",
		},
		{
			name:  "body stops the scan",
			html:  `<html><body><title>too late</title></body></html>`,
			title: "",
		},
		{
			name:  "empty document",
			html:  ``,
			title: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseHead(strings.NewReader(tc.html))
			assert.Equal(t, tc.title, got.Title)
			assert.Equal(t, tc.image, got.OGImageURL)
		})
	}
}

func TestS3776Charter_Fetch(t *testing.T) {
	t.Setenv("PREVIEW_STRICT_SSRF", "0")

	t.Run("invalid scheme", func(t *testing.T) {
		f := NewFetcher(time.Second)
		_, err := f.Fetch(context.Background(), "file:///etc/passwd")
		require.Error(t, err)
		assert.EqualError(t, err, "invalid url")
	})

	t.Run("http 500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		f := NewFetcher(2 * time.Second)
		_, err := f.Fetch(context.Background(), srv.URL)
		require.Error(t, err)
		var status *HTTPStatusError
		require.ErrorAs(t, err, &status)
		assert.Equal(t, 500, status.Code)
	})

	t.Run("html success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html><head><title>Charter</title></head></html>`)
		}))
		t.Cleanup(srv.Close)
		f := NewFetcher(2 * time.Second)
		got, err := f.Fetch(context.Background(), srv.URL)
		require.NoError(t, err)
		assert.Equal(t, "Charter", got.Title)
		assert.Empty(t, got.OEmbedURL, "OEmbedURL is an internal signal and must not leave Fetch")
	})
}

func TestS3776Charter_Process(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("non-pending is a no-op", func(t *testing.T) {
		repo := &fakePreviewRepo{work: links.PreviewWork{ID: 1, PreviewStatus: links.StatusOK}}
		w := NewWorker(nil, 1, time.Second, logger)
		w.repo = repo
		w.process(context.Background(), previewJob{id: 1, work: repo.work, claimed: true})
		assert.Empty(t, repo.updates)
	})

	t.Run("missing row is swallowed", func(t *testing.T) {
		repo := &fakePreviewRepo{getErr: errors.New("not found")}
		w := NewWorker(nil, 1, time.Second, logger)
		w.repo = repo
		w.process(context.Background(), previewJob{id: 9})
		assert.Empty(t, repo.updates)
	})
}

package changecheck

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3776Charter_ExtractMainContent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		html    string
		want    string
		notWant string
	}{
		{
			name:    "prefers main",
			html:    `<html><body><header>h</header><main>main text</main><article>art</article></body></html>`,
			want:    "main text",
			notWant: "art",
		},
		{
			name: "falls back to article",
			html: `<html><body><article>article body</article></body></html>`,
			want: "article body",
		},
		{
			name:    "falls back to body and skips script",
			html:    `<html><body><p>just a body</p><script>noise=1</script></body></html>`,
			want:    "just a body",
			notWant: "noise=1",
		},
		{
			name: "empty",
			html: ``,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractMainContent([]byte(tc.html))
			if tc.want == "" {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, tc.want)
			if tc.notWant != "" {
				assert.NotContains(t, got, tc.notWant)
			}
		})
	}
}

func TestS3776Charter_ExtractFeedURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		html string
		page string
		want string
	}{
		{
			name: "relative rss",
			html: `<html><head><link rel="alternate" type="application/rss+xml" href="/feed.xml"></head></html>`,
			page: "https://example.test/blog",
			want: "https://example.test/feed.xml",
		},
		{
			name: "atom absolute",
			html: `<html><head><link rel="alternate" type="application/atom+xml" href="https://example.test/atom"></head></html>`,
			page: "https://example.test/",
			want: "https://example.test/atom",
		},
		{
			name: "none",
			html: `<html><head><title>X</title></head><body></body></html>`,
			page: "https://x.test/",
			want: "",
		},
		{
			name: "wrong type",
			html: `<html><head><link rel="alternate" type="text/html" href="/other.html"></head></html>`,
			page: "https://x.test/",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, extractFeedURL([]byte(tc.html), tc.page))
		})
	}
}

type charterSender struct {
	mu   sync.Mutex
	got  []Notification
	err  error
	done chan struct{}
}

func (s *charterSender) Notify(_ context.Context, n Notification) error {
	s.mu.Lock()
	s.got = append(s.got, n)
	s.mu.Unlock()
	if s.done != nil {
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	}
	return s.err
}

func TestS3776Charter_PushLoop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sender := &charterSender{done: make(chan struct{})}
	w := New(nil, nil, sender, Options{Concurrency: 1}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	w.wg.Add(1)
	go w.pushLoop(ctx)

	w.pushJobs <- Notification{LinkID: 42, Title: "updated", Kind: "change_detected"}
	select {
	case <-sender.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pushLoop did not deliver")
	}
	cancel()
	w.wg.Wait()

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.got, 1)
	assert.Equal(t, int64(42), sender.got[0].LinkID)
}

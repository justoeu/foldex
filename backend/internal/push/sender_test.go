package push

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSubStore struct {
	mu      sync.Mutex
	subs    []Subscription
	deleted []string
	usedIDs []int64
	listErr error
}

func (s *fakeSubStore) List(_ context.Context) ([]Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]Subscription(nil), s.subs...), nil
}

func (s *fakeSubStore) DeleteByEndpoint(_ context.Context, endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, endpoint)
	return nil
}

func (s *fakeSubStore) MarkUsed(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usedIDs = append(s.usedIDs, id)
	return nil
}

// fakeNotifier intercepts webpush dispatch BEFORE encryption — bypasses
// webpush-go's p256dh point validation so tests don't need real ECDH keys.
// Records each subscription endpoint targeted and emits a synthetic
// HTTP response with the configured status (or per-call round-robin from
// `statuses`).
type fakeNotifier struct {
	mu       sync.Mutex
	status   int
	statuses []int
	called   []string
	err      error
}

func (f *fakeNotifier) notify(_ context.Context, _ []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = append(f.called, sub.Endpoint)
	if f.err != nil {
		return nil, f.err
	}
	st := f.status
	if len(f.statuses) > 0 {
		st = f.statuses[(len(f.called)-1)%len(f.statuses)]
	}
	return &http.Response{
		StatusCode: st,
		Body:       newNopBody(),
		Header:     make(http.Header),
	}, nil
}

func (f *fakeNotifier) endpoints() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.called...)
}

func TestSender_Notify_FansOutAcrossAllSubs(t *testing.T) {
	repo := &fakeSubStore{
		subs: []Subscription{
			fakeSub("https://push.example/a"),
			fakeSub("https://push.example/b"),
		},
	}
	n := &fakeNotifier{status: 201}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	err := s.Notify(context.Background(), Notification{LinkID: 1, Title: "x", URL: "/x", Kind: "test"})
	require.NoError(t, err)

	got := n.endpoints()
	assert.Len(t, got, 2)
	assert.ElementsMatch(t, []string{"https://push.example/a", "https://push.example/b"}, got)
}

func TestSender_Notify_NoSubsIsNoOp(t *testing.T) {
	repo := &fakeSubStore{}
	n := &fakeNotifier{status: 201}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	err := s.Notify(context.Background(), Notification{})
	require.NoError(t, err)
	assert.Empty(t, n.endpoints())
}

func TestSender_Notify_410ResponseDeletesSubscription(t *testing.T) {
	repo := &fakeSubStore{subs: []Subscription{fakeSub("https://push.example/dead")}}
	n := &fakeNotifier{status: http.StatusGone}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	err := s.Notify(context.Background(), Notification{})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://push.example/dead"}, repo.deleted)
}

func TestSender_Notify_404ResponseDeletesSubscription(t *testing.T) {
	repo := &fakeSubStore{subs: []Subscription{fakeSub("https://push.example/x")}}
	n := &fakeNotifier{status: http.StatusNotFound}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	require.NoError(t, s.Notify(context.Background(), Notification{}))
	assert.Equal(t, []string{"https://push.example/x"}, repo.deleted)
}

func TestSender_Notify_2xxMarksUsed(t *testing.T) {
	repo := &fakeSubStore{
		subs: []Subscription{
			{ID: 7, Endpoint: "https://push.example/ok", P256dh: "p", Auth: "a"},
		},
	}
	n := &fakeNotifier{status: 201}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	require.NoError(t, s.Notify(context.Background(), Notification{}))
	assert.Equal(t, []int64{7}, repo.usedIDs)
}

func TestSender_Notify_TransportErrorDoesNotBubble(t *testing.T) {
	repo := &fakeSubStore{subs: []Subscription{fakeSub("https://push.example/timeout")}}
	n := &fakeNotifier{err: errors.New("connection reset")}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	err := s.Notify(context.Background(), Notification{})
	assert.NoError(t, err, "transport errors must not bubble — only repo-level errors do")
	assert.Empty(t, repo.deleted, "transient error must NOT delete the subscription")
	assert.Empty(t, repo.usedIDs, "transient error must NOT bump last_used_at")
}

func TestSender_Notify_RepoListErrorReturned(t *testing.T) {
	repo := &fakeSubStore{listErr: errors.New("db down")}
	s := NewSender(testKeys(), repo, testLogger())

	err := s.Notify(context.Background(), Notification{})
	require.Error(t, err)
}

func TestSender_Notify_MixedStatuses(t *testing.T) {
	repo := &fakeSubStore{
		subs: []Subscription{
			fakeSub("https://push.example/keep"),
			fakeSub("https://push.example/gone"),
			fakeSub("https://push.example/keep2"),
		},
	}
	n := &fakeNotifier{statuses: []int{201, 410, 201}}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	require.NoError(t, s.Notify(context.Background(), Notification{}))
	assert.Equal(t, []string{"https://push.example/gone"}, repo.deleted)
	assert.Len(t, repo.usedIDs, 2, "two 201 responses must mark two subs used")
}

// ---- helpers ----

func fakeSub(endpoint string) Subscription {
	// p256dh/auth are arbitrary strings here — the fakeNotifier intercepts
	// before webpush-go validates the ECDH point.
	return Subscription{Endpoint: endpoint, P256dh: "p", Auth: "a", CreatedAt: time.Now()}
}

func testKeys() VAPIDKeys {
	return VAPIDKeys{
		PublicKey:  "PUB",
		PrivateKey: "PRIV",
		Subject:    "mailto:test@example.com",
	}
}

func newNopBody() *nopBody { return &nopBody{r: strings.NewReader("")} }

type nopBody struct{ r *strings.Reader }

func (n *nopBody) Read(p []byte) (int, error) { return n.r.Read(p) }
func (n *nopBody) Close() error               { return nil }

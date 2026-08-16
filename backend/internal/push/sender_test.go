package push

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

type fakeSubStore struct {
	mu            sync.Mutex
	subs          []Subscription
	listedUIDs    []authctx.UserID
	deletedUIDs   []authctx.UserID
	deletedIDs    []int64
	deleteBatches [][]int64
	usedUIDs      []authctx.UserID
	usedIDs       []int64
	usedBatches   [][]int64
	listErr       error
	deleteErr     error
	usedErr       error
}

func (s *fakeSubStore) List(_ context.Context, uid authctx.UserID) ([]Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listedUIDs = append(s.listedUIDs, uid)
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]Subscription(nil), s.subs...), nil
}

func (s *fakeSubStore) listedUsers() []authctx.UserID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authctx.UserID(nil), s.listedUIDs...)
}

func (s *fakeSubStore) DeleteGone(_ context.Context, uid authctx.UserID, ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedUIDs = append(s.deletedUIDs, uid)
	s.deletedIDs = append(s.deletedIDs, ids...)
	s.deleteBatches = append(s.deleteBatches, append([]int64(nil), ids...))
	return s.deleteErr
}

func (s *fakeSubStore) MarkUsed(_ context.Context, uid authctx.UserID, ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usedUIDs = append(s.usedUIDs, uid)
	s.usedIDs = append(s.usedIDs, ids...)
	s.usedBatches = append(s.usedBatches, append([]int64(nil), ids...))
	return s.usedErr
}

// fakeNotifier intercepts webpush dispatch BEFORE encryption — bypasses
// webpush-go's p256dh point validation so tests don't need real ECDH keys.
// Records each subscription endpoint targeted and emits a synthetic
// HTTP response with the configured default or endpoint-specific status.
type fakeNotifier struct {
	mu               sync.Mutex
	status           int
	statusByEndpoint map[string]int
	called           []string
	err              error
}

func (f *fakeNotifier) notify(_ context.Context, _ []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = append(f.called, sub.Endpoint)
	if f.err != nil {
		return nil, f.err
	}
	st := f.status
	if endpointStatus, ok := f.statusByEndpoint[sub.Endpoint]; ok {
		st = endpointStatus
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

type boundedNotifier struct {
	mu        sync.Mutex
	gate      <-chan struct{}
	started   int
	active    int
	maxActive int
}

func (n *boundedNotifier) notify(ctx context.Context, _ []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
	n.mu.Lock()
	n.started++
	n.active++
	if n.active > n.maxActive {
		n.maxActive = n.active
	}
	n.mu.Unlock()

	select {
	case <-n.gate:
	case <-ctx.Done():
		n.finish()
		return nil, ctx.Err()
	}
	n.finish()
	status := http.StatusCreated
	if strings.Contains(sub.Endpoint, "/gone/") {
		status = http.StatusGone
	}
	return &http.Response{StatusCode: status, Body: newNopBody(), Header: make(http.Header)}, nil
}

func (n *boundedNotifier) finish() {
	n.mu.Lock()
	n.active--
	n.mu.Unlock()
}

func (n *boundedNotifier) startedCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.started
}

func (n *boundedNotifier) maxActiveCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.maxActive
}

type deadlineNotifier struct {
	healthy chan string
}

func (n *deadlineNotifier) notify(ctx context.Context, _ []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
	if strings.HasSuffix(sub.Endpoint, "/slow") {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	n.healthy <- sub.Endpoint
	return &http.Response{StatusCode: http.StatusCreated, Body: newNopBody(), Header: make(http.Header)}, nil
}

func TestSender_Notify_FansOutAcrossAllSubs(t *testing.T) {
	owner := authctx.UserID(73)
	repo := &fakeSubStore{
		subs: []Subscription{
			fakeSub("https://push.example/a"),
			fakeSub("https://push.example/b"),
		},
	}
	n := &fakeNotifier{status: 201}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	err := s.Notify(context.Background(), Notification{
		LinkID: 1,
		Title:  "x",
		URL:    "/x",
		Kind:   "test",
		UserID: owner,
	})
	require.NoError(t, err)

	assert.Equal(t, []authctx.UserID{owner}, repo.listedUsers())
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
	repo := &fakeSubStore{subs: []Subscription{fakeSubWithID(41, "https://push.example/dead")}}
	n := &fakeNotifier{status: http.StatusGone}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	err := s.Notify(context.Background(), Notification{})
	require.NoError(t, err)
	assert.Equal(t, []int64{41}, repo.deletedIDs)
}

func TestSender_Notify_404ResponseDeletesSubscription(t *testing.T) {
	repo := &fakeSubStore{subs: []Subscription{fakeSubWithID(42, "https://push.example/x")}}
	n := &fakeNotifier{status: http.StatusNotFound}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	require.NoError(t, s.Notify(context.Background(), Notification{}))
	assert.Equal(t, []int64{42}, repo.deletedIDs)
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
	assert.Empty(t, repo.deletedIDs, "transient error must NOT delete the subscription")
	assert.Empty(t, repo.usedIDs, "transient error must NOT bump last_used_at")
}

func TestSender_Notify_DoesNotLogCapabilityEndpoint(t *testing.T) {
	const endpoint = "https://push.example/private-capability-token"
	repo := &fakeSubStore{subs: []Subscription{fakeSubWithID(71, endpoint)}}
	n := &fakeNotifier{err: fmt.Errorf("post %s: connection reset", endpoint)}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	s := NewSender(testKeys(), repo, logger).WithNotifyFunc(n.notify)

	require.NoError(t, s.Notify(context.Background(), Notification{}))
	assert.NotContains(t, logs.String(), endpoint)
	assert.Contains(t, logs.String(), "subscription_id=71")
}

func TestNewSenderDisablesHTTPRedirects(t *testing.T) {
	s := NewSender(testKeys(), &fakeSubStore{}, testLogger())
	client, ok := s.client.(*http.Client)
	require.True(t, ok)
	require.NotNil(t, client.CheckRedirect)

	err := client.CheckRedirect(&http.Request{}, nil)
	assert.ErrorIs(t, err, http.ErrUseLastResponse)
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
			fakeSubWithID(1, "https://push.example/keep"),
			fakeSubWithID(2, "https://push.example/gone"),
			fakeSubWithID(3, "https://push.example/keep2"),
		},
	}
	n := &fakeNotifier{status: 201, statusByEndpoint: map[string]int{"https://push.example/gone": 410}}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	require.NoError(t, s.Notify(context.Background(), Notification{}))
	assert.Equal(t, []int64{2}, repo.deletedIDs)
	assert.Len(t, repo.usedIDs, 2, "two 201 responses must mark two subs used")
}

func TestSender_Notify_Non2xxDoesNotMarkOrDelete(t *testing.T) {
	repo := &fakeSubStore{subs: []Subscription{fakeSub("https://push.example/5xx")}}
	n := &fakeNotifier{status: http.StatusInternalServerError}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)
	require.NoError(t, s.Notify(context.Background(), Notification{}))
	assert.Empty(t, repo.deletedIDs)
	assert.Empty(t, repo.usedIDs)
}

func TestSender_Notify_DeleteGoneErrorLogged(t *testing.T) {
	repo := &fakeSubStore{
		subs:      []Subscription{fakeSub("https://push.example/gone")},
		deleteErr: errors.New("db delete fail"),
	}
	// extend fake to return delete error
	n := &fakeNotifier{status: http.StatusGone}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)
	require.NoError(t, s.Notify(context.Background(), Notification{}))
}

func TestSender_Notify_MarkUsedErrorLogged(t *testing.T) {
	repo := &fakeSubStore{
		subs:    []Subscription{{ID: 1, Endpoint: "https://push.example/ok", P256dh: "p", Auth: "a"}},
		usedErr: errors.New("mark fail"),
	}
	n := &fakeNotifier{status: 201}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)
	require.NoError(t, s.Notify(context.Background(), Notification{}))
}

func TestSender_Notify_UsesBoundedParallelFanoutAndBatchedStateWrites(t *testing.T) {
	owner := authctx.UserID(91)
	repo := &fakeSubStore{}
	for i := int64(1); i <= 8; i++ {
		kind := "used"
		if i%2 == 0 {
			kind = "gone"
		}
		repo.subs = append(repo.subs, fakeSubWithID(i, fmt.Sprintf("https://push.example/%s/%d", kind, i)))
	}

	gate := make(chan struct{})
	n := &boundedNotifier{gate: gate}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)
	done := make(chan error, 1)
	go func() {
		done <- s.Notify(context.Background(), Notification{UserID: owner})
	}()

	require.Eventually(t, func() bool { return n.startedCount() == fanoutConcurrency }, time.Second, time.Millisecond)
	assert.Equal(t, fanoutConcurrency, n.maxActiveCount())
	close(gate)
	require.NoError(t, <-done)

	assert.Equal(t, fanoutConcurrency, n.maxActiveCount())
	assert.ElementsMatch(t, []int64{1, 3, 5, 7}, repo.usedIDs)
	assert.ElementsMatch(t, []int64{2, 4, 6, 8}, repo.deletedIDs)
	assert.Equal(t, []authctx.UserID{owner}, repo.usedUIDs)
	assert.Equal(t, []authctx.UserID{owner}, repo.deletedUIDs)
	assert.Len(t, repo.usedBatches, 1, "successful deliveries must use one database round trip")
	assert.Len(t, repo.deleteBatches, 1, "gone deliveries must use one database round trip")
}

func TestSender_Notify_BoundsConcurrentFanoutsGlobally(t *testing.T) {
	repo := &fakeSubStore{}
	for i := int64(1); i <= 8; i++ {
		repo.subs = append(repo.subs, fakeSubWithID(i, fmt.Sprintf("https://push.example/%d", i)))
	}
	gate := make(chan struct{})
	n := &boundedNotifier{gate: gate}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)
	done := make(chan error, 2)
	go func() { done <- s.Notify(context.Background(), Notification{UserID: 1}) }()
	go func() { done <- s.Notify(context.Background(), Notification{UserID: 2}) }()

	require.Eventually(t, func() bool { return n.startedCount() >= fanoutConcurrency }, time.Second, time.Millisecond)
	assert.Never(t, func() bool { return n.maxActiveCount() > fanoutConcurrency }, 50*time.Millisecond, time.Millisecond)
	close(gate)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	assert.LessOrEqual(t, n.maxActiveCount(), fanoutConcurrency)
}

func TestSender_Notify_DefensivelyCapsStoreResults(t *testing.T) {
	repo := &fakeSubStore{}
	for i := int64(1); i <= MaxSubscriptionsPerUser+3; i++ {
		repo.subs = append(repo.subs, fakeSubWithID(i, fmt.Sprintf("https://push.example/%d", i)))
	}
	n := &fakeNotifier{status: http.StatusCreated}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)

	require.NoError(t, s.Notify(context.Background(), Notification{UserID: authctx.UserID(1)}))
	assert.Len(t, n.endpoints(), MaxSubscriptionsPerUser)
	assert.Len(t, repo.usedIDs, MaxSubscriptionsPerUser)
	assert.Len(t, repo.usedBatches, 1)
}

func TestSender_Notify_SlowEndpointDoesNotStarveHealthyEndpointsBeforeDeadline(t *testing.T) {
	repo := &fakeSubStore{subs: []Subscription{
		fakeSubWithID(1, "https://push.example/slow"),
		fakeSubWithID(2, "https://push.example/healthy/2"),
		fakeSubWithID(3, "https://push.example/healthy/3"),
		fakeSubWithID(4, "https://push.example/healthy/4"),
		fakeSubWithID(5, "https://push.example/healthy/5"),
	}}
	n := &deadlineNotifier{healthy: make(chan string, 4)}
	s := NewSender(testKeys(), repo, testLogger()).WithNotifyFunc(n.notify)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	done := make(chan error, 1)
	go func() { done <- s.Notify(ctx, Notification{}) }()

	for i := 0; i < 4; i++ {
		select {
		case <-n.healthy:
		case <-time.After(time.Second):
			t.Fatal("healthy endpoint was starved behind the slow endpoint")
		}
	}
	cancel()
	require.NoError(t, <-done)
}

func TestAsStdClient_ConcreteAndDoer(t *testing.T) {
	c := &http.Client{Timeout: time.Second}
	assert.Same(t, c, asStdClient(c))

	var hit bool
	fake := doerFunc(func(req *http.Request) (*http.Response, error) {
		hit = true
		return &http.Response{StatusCode: 200, Body: newNopBody(), Header: make(http.Header)}, nil
	})
	adapted := asStdClient(fake)
	require.NotNil(t, adapted)
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	require.NoError(t, err)
	resp, err := adapted.Transport.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.True(t, hit)
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

// ---- helpers ----

func fakeSub(endpoint string) Subscription {
	// p256dh/auth are arbitrary strings here — the fakeNotifier intercepts
	// before webpush-go validates the ECDH point.
	return fakeSubWithID(1, endpoint)
}

func fakeSubWithID(id int64, endpoint string) Subscription {
	return Subscription{ID: id, Endpoint: endpoint, P256dh: "p", Auth: "a", CreatedAt: time.Now()}
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

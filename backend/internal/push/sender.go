package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/outboundhttp"
)

const fanoutConcurrency = 4

// Notification is the payload encrypted and shipped to every live
// subscription. The kind discriminator lets the SW pick a UI variant in the
// future without changing the wire format.
type Notification struct {
	LinkID int64  `json:"link_id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Kind   string `json:"kind"`
	// UserID scopes the fan-out to the link owner's subscriptions. It is
	// json:"-" so it never reaches the browser payload — the client already
	// knows who it is, and echoing an internal id buys nothing.
	UserID authctx.UserID `json:"-"`
}

// SubscriptionStore is the contract the sender needs from the repo. Kept
// tiny so the test sender can mock it without standing up Postgres.
type SubscriptionStore interface {
	List(ctx context.Context, uid authctx.UserID) ([]Subscription, error)
	DeleteGone(ctx context.Context, uid authctx.UserID, ids []int64) error
	MarkUsed(ctx context.Context, uid authctx.UserID, ids []int64) error
}

// HTTPDoer is the minimal http.Client surface used by the sender — swap in
// a fake for tests so we don't depend on a real Push service.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Sender fans a Notification out across all live subscriptions. Best-effort
// — a single endpoint failure logs and continues; 404/410 prunes the row;
// other 4xx/5xx are surfaced as warnings.
type Sender struct {
	keys          VAPIDKeys
	repo          SubscriptionStore
	client        HTTPDoer
	logger        *slog.Logger
	ttl           int
	deliverySlots chan struct{}
	// notify is the actual webpush dispatcher. Defaulted to
	// webpush.SendNotificationWithContext + the package's http client; tests
	// override this so they don't have to provide real ECDH-valid p256dh
	// keys (webpush-go pre-flight validates the point before any HTTP).
	notify func(ctx context.Context, payload []byte, sub *webpush.Subscription, opts *webpush.Options) (*http.Response, error)
}

func NewSender(keys VAPIDKeys, repo SubscriptionStore, logger *slog.Logger) *Sender {
	s := &Sender{
		keys: keys,
		repo: repo,
		client: &http.Client{
			Timeout:       10 * time.Second,
			Transport:     outboundhttp.NewPublicTransport(10 * time.Second),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		logger:        logger.With("component", "push"),
		ttl:           60 * 60 * 24, // 24h — the push service holds the message for at most a day
		deliverySlots: make(chan struct{}, fanoutConcurrency),
	}
	s.notify = webpush.SendNotificationWithContext
	return s
}

// WithNotifyFunc lets tests bypass the real webpush dispatcher (which
// requires a valid ECDH p256dh point before reaching the HTTP layer) so
// status-routing logic can be exercised without crypto fixtures.
func (s *Sender) WithNotifyFunc(
	fn func(ctx context.Context, payload []byte, sub *webpush.Subscription, opts *webpush.Options) (*http.Response, error),
) *Sender {
	s.notify = fn
	return s
}

// Notify encrypts and sends `n` to every live subscription. Errors are
// logged per-endpoint and never returned — single-link failure should never
// abort the rest of the fan-out. The aggregate return value is non-nil
// only when the repo lookup itself fails (an actually unactionable error).
func (s *Sender) Notify(ctx context.Context, n Notification) error {
	subs, err := s.repo.List(ctx, n.UserID)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	if len(subs) == 0 {
		return nil
	}
	if len(subs) > MaxSubscriptionsPerUser {
		subs = subs[:MaxSubscriptionsPerUser]
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	results := s.fanOut(ctx, subs, payload)
	s.persistResults(ctx, n.UserID, results)
	return nil
}

func (s *Sender) fanOut(ctx context.Context, subs []Subscription, payload []byte) []deliveryResult {
	jobs := make(chan Subscription, len(subs))
	results := make(chan deliveryResult, len(subs))
	for _, sub := range subs {
		jobs <- sub
	}
	close(jobs)

	workers := min(fanoutConcurrency, len(subs))
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go s.runFanoutWorker(ctx, jobs, results, payload, &wg)
	}
	wg.Wait()
	close(results)

	out := make([]deliveryResult, 0, len(results))
	for result := range results {
		out = append(out, result)
	}
	return out
}

func (s *Sender) runFanoutWorker(
	ctx context.Context,
	jobs <-chan Subscription,
	results chan<- deliveryResult,
	payload []byte,
	wg *sync.WaitGroup,
) {
	defer wg.Done()
	for sub := range jobs {
		if ctx.Err() != nil {
			return
		}
		select {
		case s.deliverySlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		result := s.sendOne(ctx, sub, payload)
		<-s.deliverySlots
		if result.state != deliveryUnchanged {
			results <- result
		}
	}
}

func (s *Sender) persistResults(ctx context.Context, uid authctx.UserID, results []deliveryResult) {
	var usedIDs, goneIDs []int64
	for _, result := range results {
		switch result.state {
		case deliveryUsed:
			usedIDs = append(usedIDs, result.id)
		case deliveryGone:
			goneIDs = append(goneIDs, result.id)
		}
	}
	if len(usedIDs) > 0 {
		if err := s.repo.MarkUsed(ctx, uid, usedIDs); err != nil {
			s.logger.Warn("push mark-used batch failed", "count", len(usedIDs), "err", err)
		}
	}
	if len(goneIDs) > 0 {
		if err := s.repo.DeleteGone(ctx, uid, goneIDs); err != nil {
			s.logger.Warn("push delete-gone batch failed", "count", len(goneIDs), "err", err)
		} else {
			s.logger.Info("push subscriptions removed (gone)", "count", len(goneIDs))
		}
	}
}

type deliveryState uint8

const (
	deliveryUnchanged deliveryState = iota
	deliveryUsed
	deliveryGone
)

type deliveryResult struct {
	id    int64
	state deliveryState
}

func (s *Sender) sendOne(ctx context.Context, sub Subscription, payload []byte) deliveryResult {
	wpSub := &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys: webpush.Keys{
			P256dh: sub.P256dh,
			Auth:   sub.Auth,
		},
	}
	opts := &webpush.Options{
		Subscriber:      s.keys.Subject,
		VAPIDPublicKey:  s.keys.PublicKey,
		VAPIDPrivateKey: s.keys.PrivateKey,
		TTL:             s.ttl,
		HTTPClient:      asStdClient(s.client),
	}
	resp, err := s.notify(ctx, payload, wpSub, opts)
	if err != nil {
		s.logger.Warn("push send failed", "subscription_id", sub.ID, "reason", pushErrorReason(err))
		return deliveryResult{}
	}
	defer resp.Body.Close()
	// Drain to allow connection reuse. Cap to 4 KiB — push services return
	// tiny text bodies; if a malicious endpoint streamed a multi-MB response
	// we'd otherwise tie up the worker.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return deliveryResult{id: sub.ID, state: deliveryGone}
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return deliveryResult{id: sub.ID, state: deliveryUsed}
	default:
		s.logger.Warn("push send non-2xx", "subscription_id", sub.ID, "status", resp.StatusCode)
		return deliveryResult{}
	}
}

func pushErrorReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "delivery_failed"
	}
}

// asStdClient adapts our minimal HTTPDoer interface back to the concrete
// *http.Client that webpush-go expects. The library hardcodes the type;
// we keep the indirection so test fakes work — they wrap a real Client
// with a transport hook.
func asStdClient(d HTTPDoer) *http.Client {
	if c, ok := d.(*http.Client); ok {
		return c
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: doerTransport{d: d},
	}
}

type doerTransport struct{ d HTTPDoer }

func (t doerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.d.Do(req)
}

// senderError is the error a Sender returns when the entire batch fails
// (repo unavailable). Kept exported so handlers can errors.Is against it.
var ErrRepoUnavailable = errors.New("push: subscription repo unavailable")

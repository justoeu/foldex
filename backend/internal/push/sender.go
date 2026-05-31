package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"foldex/internal/preview"
)

// Notification is the payload encrypted and shipped to every live
// subscription. The kind discriminator lets the SW pick a UI variant in the
// future without changing the wire format.
type Notification struct {
	LinkID int64  `json:"link_id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Kind   string `json:"kind"`
}

// SubscriptionStore is the contract the sender needs from the repo. Kept
// tiny so the test sender can mock it without standing up Postgres.
type SubscriptionStore interface {
	List(ctx context.Context) ([]Subscription, error)
	DeleteByEndpoint(ctx context.Context, endpoint string) error
	MarkUsed(ctx context.Context, id int64) error
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
	keys   VAPIDKeys
	repo   SubscriptionStore
	client HTTPDoer
	logger *slog.Logger
	ttl    int
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
		// SSRF-guarded transport reused from internal/preview so a
		// pwned-SHARED_SECRET attacker can't register
		// `endpoint=https://169.254.169.254/...` and force the backend
		// to POST to IMDS or any RFC1918 host. CLAUDE.md §4 invariant:
		// every outbound user-controlled URL goes through the same
		// pre-dial LookupIP + post-dial RemoteAddr checks.
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: preview.NewSafeTransport(10 * time.Second),
		},
		logger: logger.With("component", "push"),
		ttl:    60 * 60 * 24, // 24h — the push service holds the message for at most a day
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
	subs, err := s.repo.List(ctx)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	if len(subs) == 0 {
		return nil
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	for _, sub := range subs {
		s.sendOne(ctx, sub, payload)
	}
	return nil
}

// sendOne wraps webpush.SendNotificationWithContext + status routing. Kept
// separate from Notify so the inner loop body is a single call site that's
// easy to reason about.
func (s *Sender) sendOne(ctx context.Context, sub Subscription, payload []byte) {
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
		s.logger.Warn("push send failed", "endpoint", sub.Endpoint, "err", err)
		return
	}
	defer resp.Body.Close()
	// Drain to allow connection reuse. Cap to 4 KiB — push services return
	// tiny text bodies; if a malicious endpoint streamed a multi-MB response
	// we'd otherwise tie up the worker.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// The subscription is permanently gone — RFC 8030 §7.3. Delete the
		// row so the next Notify doesn't re-attempt.
		if err := s.repo.DeleteByEndpoint(ctx, sub.Endpoint); err != nil {
			s.logger.Warn("push delete-gone failed", "endpoint", sub.Endpoint, "err", err)
		} else {
			s.logger.Info("push subscription removed (gone)", "endpoint", sub.Endpoint)
		}
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if err := s.repo.MarkUsed(ctx, sub.ID); err != nil {
			s.logger.Warn("push mark-used failed", "endpoint", sub.Endpoint, "err", err)
		}
	default:
		s.logger.Warn("push send non-2xx", "endpoint", sub.Endpoint, "status", resp.StatusCode)
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

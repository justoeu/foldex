//go:build integration

package push_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/push"
	"foldex/internal/testdb"
)

func newPushRouter(t *testing.T, withSender bool) (http.Handler, *push.Repository) {
	t.Helper()
	pool := testdb.New(t)
	repo := push.NewRepository(pool)
	keys := push.VAPIDKeys{PublicKey: "PUB", PrivateKey: "PRIV", Subject: "mailto:t@h"}
	var sender *push.Sender
	if withSender {
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		sender = push.NewSender(keys, repo, logger).WithNotifyFunc(
			func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				return &http.Response{StatusCode: 201, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
			},
		)
	}
	r := chi.NewRouter()
	r.Route("/push", push.NewHandler(keys, repo, sender).Mount)
	return r, repo
}

func doPush(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHandler_SubscribeUnsubscribe_OK(t *testing.T) {
	h, repo := newPushRouter(t, false)

	rr := doPush(t, h, http.MethodPost, "/push/subscriptions", map[string]any{
		"endpoint": "https://fcm.googleapis.com/fcm/send/abc123xyz",
		"p256dh":   "p256dh-key",
		"auth":     "auth-key",
	})
	require.Equal(t, http.StatusCreated, rr.Code)
	var created struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Positive(t, created.ID)

	list, err := repo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)

	rr = doPush(t, h, http.MethodDelete, "/push/subscriptions", map[string]any{
		"endpoint": "https://fcm.googleapis.com/fcm/send/abc123xyz",
	})
	require.Equal(t, http.StatusNoContent, rr.Code)

	list, err = repo.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestHandler_Subscribe_InvalidJSON(t *testing.T) {
	h, _ := newPushRouter(t, false)
	req := httptest.NewRequest(http.MethodPost, "/push/subscriptions", bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Unsubscribe_InvalidJSON(t *testing.T) {
	h, _ := newPushRouter(t, false)
	req := httptest.NewRequest(http.MethodDelete, "/push/subscriptions", bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Test_Accepted(t *testing.T) {
	h, _ := newPushRouter(t, true)
	// Seed a subscription so Notify has work (or empty is also OK).
	_ = doPush(t, h, http.MethodPost, "/push/subscriptions", map[string]any{
		"endpoint": "https://fcm.googleapis.com/fcm/send/test-ep-long",
		"p256dh":   "k",
		"auth":     "a",
	})
	rr := doPush(t, h, http.MethodPost, "/push/test", nil)
	require.Equal(t, http.StatusAccepted, rr.Code)
}

func TestHandler_Test_RepoListError(t *testing.T) {
	// Sender with a store that fails List.
	keys := push.VAPIDKeys{PublicKey: "P", PrivateKey: "K", Subject: "mailto:t@h"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	failStore := &failListStore{}
	sender := push.NewSender(keys, failStore, logger)
	r := chi.NewRouter()
	r.Route("/push", push.NewHandler(keys, nil, sender).Mount)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/push/test", nil))
	require.NotEqual(t, http.StatusAccepted, rr.Code)
}

type failListStore struct{}

func (f *failListStore) List(context.Context) ([]push.Subscription, error) {
	return nil, assert.AnError
}
func (f *failListStore) DeleteByEndpoint(context.Context, string) error { return nil }
func (f *failListStore) MarkUsed(context.Context, int64) error          { return nil }

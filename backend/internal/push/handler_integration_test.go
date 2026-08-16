//go:build integration

package push_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authctx/authctxtest"
	"foldex/internal/push"
	"foldex/internal/testdb"
)

// TestMain owns the lifetime of this package's shared Postgres container.
//
// It cannot be a t.Cleanup: os.Exit skips deferred work, and a cleanup hung off
// whichever test ran first would tear the database down while the rest of the
// package still needed it. The Makefile disables testcontainers' reaper, so
// nothing else would collect it.
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

func newPushRouter(t *testing.T, withSender bool) (http.Handler, *push.Repository, authctx.UserID) {
	t.Helper()
	pool := testdb.Shared(t)

	uid := testdb.SeedUser(t, pool, "owner@test.local", "admin")
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
	r.Use(authctxtest.Middleware(uid))
	r.Route("/push", push.NewHandler(keys, repo, sender).Mount)
	return r, repo, uid
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
	h, repo, uid := newPushRouter(t, false)

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

	list, err := repo.List(context.Background(), uid)
	require.NoError(t, err)
	require.Len(t, list, 1)

	rr = doPush(t, h, http.MethodDelete, "/push/subscriptions", map[string]any{
		"endpoint": "https://fcm.googleapis.com/fcm/send/abc123xyz",
	})
	require.Equal(t, http.StatusNoContent, rr.Code)

	list, err = repo.List(context.Background(), uid)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestHandler_Subscribe_MapsSubscriptionLimit(t *testing.T) {
	h, _, _ := newPushRouter(t, false)
	for i := range push.MaxSubscriptionsPerUser {
		rr := doPush(t, h, http.MethodPost, "/push/subscriptions", map[string]any{
			"endpoint": fmt.Sprintf("https://push.example/handler/%d", i),
			"p256dh":   "key",
			"auth":     "auth",
		})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	}

	rr := doPush(t, h, http.MethodPost, "/push/subscriptions", map[string]any{
		"endpoint": "https://push.example/handler/overflow",
		"p256dh":   "key",
		"auth":     "auth",
	})
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), `"code":"subscription_limit_reached"`)
}

func TestHandler_Subscribe_MapsInvalidSubscription(t *testing.T) {
	h, _, _ := newPushRouter(t, false)
	rr := doPush(t, h, http.MethodPost, "/push/subscriptions", map[string]any{
		"endpoint": "https://push.example/missing-key",
		"p256dh":   "",
		"auth":     "auth",
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), `"code":"invalid_subscription"`)
}

func TestHandler_Unsubscribe_CannotDeleteAnotherUsersEndpoint(t *testing.T) {
	pool := testdb.Shared(t)
	ctx := context.Background()
	userA := testdb.SeedUser(t, pool, "user-a@test.local", "user")
	userB := testdb.SeedUser(t, pool, "user-b@test.local", "user")
	repo := push.NewRepository(pool)
	keys := push.VAPIDKeys{PublicKey: "PUB", PrivateKey: "PRIV", Subject: "mailto:t@h"}

	const endpointA = "https://push.example/users/a-endpoint"
	const endpointB = "https://push.example/users/b-endpoint"
	_, err := repo.Save(ctx, userA, endpointA, "a-p256dh", "a-auth")
	require.NoError(t, err)
	_, err = repo.Save(ctx, userB, endpointB, "b-p256dh", "b-auth")
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(authctxtest.Middleware(userA))
	r.Route("/push", push.NewHandler(keys, repo, nil).Mount)

	rr := doPush(t, r, http.MethodDelete, "/push/subscriptions", map[string]any{
		"endpoint": endpointB,
	})
	require.Equal(t, http.StatusNoContent, rr.Code)

	subsB, err := repo.List(ctx, userB)
	require.NoError(t, err)
	require.Len(t, subsB, 1)
	assert.Equal(t, endpointB, subsB[0].Endpoint)

	rr = doPush(t, r, http.MethodDelete, "/push/subscriptions", map[string]any{
		"endpoint": endpointA,
	})
	require.Equal(t, http.StatusNoContent, rr.Code)

	subsA, err := repo.List(ctx, userA)
	require.NoError(t, err)
	assert.Empty(t, subsA)
	subsB, err = repo.List(ctx, userB)
	require.NoError(t, err)
	require.Len(t, subsB, 1)
	assert.Equal(t, endpointB, subsB[0].Endpoint)
}

func TestHandler_Subscribe_InvalidJSON(t *testing.T) {
	h, _, _ := newPushRouter(t, false)
	req := httptest.NewRequest(http.MethodPost, "/push/subscriptions", bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Unsubscribe_InvalidJSON(t *testing.T) {
	h, _, _ := newPushRouter(t, false)
	req := httptest.NewRequest(http.MethodDelete, "/push/subscriptions", bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandler_Test_Accepted(t *testing.T) {
	h, _, _ := newPushRouter(t, true)
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
	r.Use(authctxtest.Middleware(authctxtest.DefaultUser))
	r.Route("/push", push.NewHandler(keys, nil, sender).Mount)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/push/test", nil))
	require.NotEqual(t, http.StatusAccepted, rr.Code)
}

type failListStore struct{}

func (f *failListStore) List(context.Context, authctx.UserID) ([]push.Subscription, error) {
	return nil, assert.AnError
}
func (f *failListStore) DeleteGone(context.Context, authctx.UserID, []int64) error { return nil }
func (f *failListStore) MarkUsed(context.Context, authctx.UserID, []int64) error   { return nil }

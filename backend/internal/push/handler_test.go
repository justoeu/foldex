package push

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func newTestHandler() *Handler {
	return NewHandler(
		VAPIDKeys{PublicKey: "PUB", PrivateKey: "PRIV", Subject: "mailto:t@h"},
		nil, // repo: subscribe/unsubscribe tests use the mux-mounted handler with a nil repo
		nil,
	)
}

func TestHandler_VapidKey_ReturnsPublicKey(t *testing.T) {
	h := newTestHandler()
	r := chi.NewRouter()
	r.Route("/push", h.Mount)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/push/vapid-key", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"public_key":"PUB"`)
}

func TestHandler_Subscribe_RejectsNonHTTPSEndpoint(t *testing.T) {
	h := newTestHandler()
	r := chi.NewRouter()
	r.Route("/push", h.Mount)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/push/subscriptions",
		bytes.NewBufferString(`{"endpoint":"http://insecure/x","p256dh":"k","auth":"a"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_endpoint")
}

func TestHandler_Subscribe_RejectsShortEndpoint(t *testing.T) {
	h := newTestHandler()
	r := chi.NewRouter()
	r.Route("/push", h.Mount)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/push/subscriptions",
		bytes.NewBufferString(`{"endpoint":"https://x","p256dh":"k","auth":"a"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_Subscribe_RejectsMalformedJSON(t *testing.T) {
	h := newTestHandler()
	r := chi.NewRouter()
	r.Route("/push", h.Mount)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/push/subscriptions",
		bytes.NewBufferString(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_json")
}

func TestHandler_Unsubscribe_RejectsEmptyEndpoint(t *testing.T) {
	h := newTestHandler()
	r := chi.NewRouter()
	r.Route("/push", h.Mount)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/push/subscriptions",
		bytes.NewBufferString(`{"endpoint":""}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_Test_ReturnsServiceUnavailableWhenSenderNil(t *testing.T) {
	h := NewHandler(VAPIDKeys{}, nil, nil)
	r := chi.NewRouter()
	r.Route("/push", h.Mount)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/push/test", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "push_disabled")
}

func TestHandler_TestRejectsWhenGlobalAdmissionIsFull(t *testing.T) {
	sender := NewSender(VAPIDKeys{}, &fakeSubStore{}, testLogger())
	h := NewHandler(VAPIDKeys{}, nil, sender)
	for range cap(h.testSlots) {
		h.testSlots <- struct{}{}
	}
	r := chi.NewRouter()
	r.Route("/push", h.Mount)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/push/test", nil))

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "push_busy")
}

func TestHandler_TestBoundsFanoutWithRequestDeadline(t *testing.T) {
	repo := &fakeSubStore{subs: []Subscription{fakeSub("https://push.example/test")}}
	var hasDeadline bool
	sender := NewSender(VAPIDKeys{}, repo, testLogger()).WithNotifyFunc(
		func(ctx context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
			_, hasDeadline = ctx.Deadline()
			return &http.Response{StatusCode: http.StatusCreated, Body: newNopBody(), Header: make(http.Header)}, nil
		},
	)
	h := NewHandler(VAPIDKeys{}, nil, sender)
	r := chi.NewRouter()
	r.Route("/push", h.Mount)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/push/test", nil)
	req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{UserID: 1}))
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.True(t, hasDeadline)
}

func TestIsValidPushEndpoint(t *testing.T) {
	cases := []struct {
		endpoint string
		want     bool
	}{
		{"https://fcm.googleapis.com/fcm/send/abc123", true},
		{"http://insecure/x", false},
		{"", false},
		{"https://", false},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.want, isValidPushEndpoint(tc.endpoint), "endpoint=%q", tc.endpoint)
	}
}

package push

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
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

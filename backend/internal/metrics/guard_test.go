package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBearerTokenGuard(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	disabled := httptest.NewRecorder()
	BearerTokenGuard("", "bridge disabled", next).ServeHTTP(disabled, httptest.NewRequest(http.MethodGet, "/", nil))
	if disabled.Code != http.StatusServiceUnavailable || !strings.Contains(disabled.Body.String(), "bridge disabled") {
		t.Fatalf("disabled = %d %q, want 503 with the caller's message", disabled.Code, disabled.Body.String())
	}

	unauthorized := httptest.NewRecorder()
	BearerTokenGuard("secret", "bridge disabled", next).ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("unauthorized = %d challenge=%q", unauthorized.Code, unauthorized.Header().Get("WWW-Authenticate"))
	}

	ok := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	token := "secret"
	req.Header.Set("Authorization", "Bearer "+token)
	BearerTokenGuard(token, "bridge disabled", next).ServeHTTP(ok, req)
	if ok.Code != http.StatusTeapot {
		t.Fatalf("authorized = %d, want next handler's status", ok.Code)
	}
}

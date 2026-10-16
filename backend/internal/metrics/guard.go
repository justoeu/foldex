package metrics

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// BearerTokenGuard wraps next behind a token check. Empty token → 503 with
// disabledMessage as the body (the endpoint does not exist — there is no
// anonymous mode). Wrong or missing token → 401 with a Bearer challenge. The
// comparison is constant-time so the token cannot be recovered by timing.
func BearerTokenGuard(token, disabledMessage string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, disabledMessage, http.StatusServiceUnavailable)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PromHandler serves reg in Prometheus format behind BearerTokenGuard.
// MaxRequestsInFlight caps concurrent scrapes at 1: a scrape walks every
// collector, and an attacker who guessed the token could otherwise stack
// Gather calls. Timeout keeps a slow-reading scraper from parking on that
// single in-flight slot for the server's full WriteTimeout.
func PromHandler(reg *prometheus.Registry, token string) http.Handler {
	inner := promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		MaxRequestsInFlight: 1,
		Timeout:             10 * time.Second,
	})
	return BearerTokenGuard(token, "metrics disabled", inner)
}

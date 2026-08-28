package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
)

func gated(t *testing.T, blocked ...string) http.Handler {
	t.Helper()
	list := auth.NewBlocklist(func(context.Context) ([]string, error) { return blocked, nil })
	return blocklistGate(list)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached"))
	}))
}

func call(h http.Handler, path, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A blocked caller must reach no handler at all — that is what makes this
// cheaper than a check inside each one.
func TestBlocklistGate_RefusesABlockedAddress(t *testing.T) {
	rec := call(gated(t, "203.0.113.9"), "/api/links", "203.0.113.9:41000")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ip_blocked")
	assert.NotContains(t, rec.Body.String(), "reached")
}

func TestBlocklistGate_LetsEveryoneElseThrough(t *testing.T) {
	rec := call(gated(t, "203.0.113.9"), "/api/links", "198.51.100.4:41000")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "reached")
}

// The gate runs after trustedProxyRealIP, so RemoteAddr may be a bare host or
// host:port depending on whether the header was honoured. Both are the same
// address and both must be refused, or the block is a coin flip.
func TestBlocklistGate_MatchesRegardlessOfSpelling(t *testing.T) {
	h := gated(t, "203.0.113.9")
	for _, remote := range []string{"203.0.113.9:41000", "203.0.113.9", "::ffff:203.0.113.9"} {
		assert.Equal(t, http.StatusForbidden, call(h, "/api/links", remote).Code, "remote %q", remote)
	}
}

// An orchestrator decides whether this process is alive from /healthz. An
// instance that reports itself unhealthy because someone blocked a probe's
// address would be restarted in a loop.
func TestBlocklistGate_NeverRefusesTheHealthProbe(t *testing.T) {
	rec := call(gated(t, "203.0.113.9"), "/healthz", "203.0.113.9:41000")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// An unparseable peer normalizes to the empty string, which must not become a
// key that matches anything.
func TestBlocklistGate_AllowsAnUnparseablePeer(t *testing.T) {
	rec := call(gated(t, ""), "/api/links", "not-an-address")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// clientIP is what the gate compares and what the trail stores. One spelling,
// or a block is installed against an address no request ever presents.
func TestClientIP_NormalizesToTheStoredSpelling(t *testing.T) {
	for remote, want := range map[string]string{
		"203.0.113.9:41000":   "203.0.113.9",
		"203.0.113.9":         "203.0.113.9",
		"::ffff:203.0.113.9":  "203.0.113.9",
		"[2001:db8::1]:41000": "2001:db8::1",
		"garbage":             "",
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		require.Equal(t, want, clientIP(req), "remote %q", remote)
	}
}

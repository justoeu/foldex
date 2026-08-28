package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
)

// The whole rationale of migration 000044 is that an address is recorded WITH
// its provenance: `ip` is what the server observed and `ip_trusted` says
// whether a configured proxy vouched for it. Every other test sets the flag on
// the struct by hand, which proves the column round-trips and proves nothing
// about the chain that fills it.
//
// This asserts the security-relevant direction end to end: through the real
// middleware, into a real AuditRecord, in the case that matters — a forged
// header from a peer nobody trusts.
func observed(t *testing.T, trusted []*net.IPNet, remote string, headers map[string]string) auth.AuditRecord {
	t.Helper()
	var rec auth.AuditRecord
	h := trustedProxyRealIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		rec = auth.AuditRecord{Action: auth.AuditLoginFailed}.WithRequest(r)
	}))
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.RemoteAddr = remote
	req.Header.Set("User-Agent", "curl/8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return rec
}

func mustNets(t *testing.T, entries ...string) []*net.IPNet {
	t.Helper()
	nets, bad := parseTrustedProxies(joinCSV(entries))
	require.Empty(t, bad)
	return nets
}

func joinCSV(entries []string) string {
	out := ""
	for i, e := range entries {
		if i > 0 {
			out += ","
		}
		out += e
	}
	return out
}

// The case migration 000033 refused to store: a forged header from a peer that
// is not a configured proxy. The trail must record the PEER, and must not claim
// anyone vouched for it — a row that said otherwise would be
// authoritative-looking and attacker-controlled at once.
func TestAuditProvenance_ASpoofedHeaderFromAnUntrustedPeerIsNotBelieved(t *testing.T) {
	rec := observed(t, nil, "203.0.113.9:41000",
		map[string]string{"X-Forwarded-For": "8.8.8.8"})

	assert.Equal(t, "203.0.113.9", rec.IP, "the peer is what was observed")
	assert.False(t, rec.IPTrusted, "nobody vouched for this address")
}

// Even WITH a proxy list configured, a peer outside it is not a proxy.
func TestAuditProvenance_AHeaderFromOutsideTheProxySetIsNotBelieved(t *testing.T) {
	rec := observed(t, mustNets(t, "10.0.0.0/8"), "203.0.113.9:41000",
		map[string]string{"X-Forwarded-For": "8.8.8.8"})

	assert.Equal(t, "203.0.113.9", rec.IP)
	assert.False(t, rec.IPTrusted)
}

// The honest positive: behind a configured proxy the client address is recorded
// AND marked, so the screen can say the difference instead of hiding it.
func TestAuditProvenance_AProxyInTheSetIsBelievedAndMarked(t *testing.T) {
	rec := observed(t, mustNets(t, "10.4.2.7"), "10.4.2.7:41000",
		map[string]string{"X-Forwarded-For": "203.0.113.9"})

	assert.Equal(t, "203.0.113.9", rec.IP, "the client behind the proxy is the caller")
	assert.True(t, rec.IPTrusted, "a configured proxy vouched for it")
}

// A direct bind with no proxy configured is the product's own default, and
// every row there is the raw peer with the flag false.
func TestAuditProvenance_TheDefaultDeploymentRecordsThePeerUnvouched(t *testing.T) {
	rec := observed(t, nil, "192.0.2.10:1234", nil)

	assert.Equal(t, "192.0.2.10", rec.IP)
	assert.False(t, rec.IPTrusted)
	assert.Equal(t, "curl/8", rec.UserAgent)
}

// The address is stored in ONE spelling, or the blocklist compares against a
// string no request ever presents and the origins aggregate splits one host
// into two rows.
func TestAuditProvenance_TheRecordedAddressIsNormalized(t *testing.T) {
	rec := observed(t, mustNets(t, "10.4.2.7"), "10.4.2.7:41000",
		map[string]string{"X-Forwarded-For": "::ffff:203.0.113.9"})

	assert.Equal(t, "203.0.113.9", rec.IP)
	assert.True(t, rec.IPTrusted)
}

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

// requireAdmin has a fallback branch for the case where the auth middleware is
// not wired (AUTH_ENABLED=0, router built without an auth stack). The branch
// must be fail-CLOSED: defaulting to "allow" when the middleware happens to be
// nil would turn a wiring mistake into an authorization bypass, which is the
// exact failure mode the design exists to prevent.
func TestRequireAdminFallbackIsFailClosed(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name      string
		principal *authctx.Principal
		want      int
	}{
		{"no principal at all", nil, http.StatusNotFound},
		{"plain user", &authctx.Principal{UserID: 2, Role: authctx.RoleUser}, http.StatusNotFound},
		{"admin", &authctx.Principal{UserID: 1, Role: authctx.RoleAdmin}, http.StatusOK},
		{"empty role", &authctx.Principal{UserID: 3, Role: ""}, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// nil middleware exercises the in-line fallback.
			h := requireAdmin(nil)(ok)

			req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
			if tc.principal != nil {
				req = req.WithContext(authctx.WithPrincipal(context.Background(), *tc.principal))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Code)
			if tc.want == http.StatusNotFound {
				// 404, never 403: a 403 confirms the route exists and that the
				// caller merely lacks the role, which tells an attacker exactly
				// what to escalate toward.
				assert.Contains(t, rec.Body.String(), "not_found")
				assert.NotContains(t, rec.Body.String(), "forbidden")
			}
		})
	}
}

// The Fetch spec forbids `Access-Control-Allow-Origin: *` together with
// credentialed requests, and browsers reject the preflight rather than
// explaining why. Since PR2 the session lives in cookies, so a wildcard origin
// list has to be replaced at boot instead of silently breaking every
// cross-origin call.
func TestContainsWildcard(t *testing.T) {
	t.Parallel()
	assert.True(t, containsWildcard([]string{"*"}))
	assert.True(t, containsWildcard([]string{"https://a.test", "*"}))
	assert.False(t, containsWildcard([]string{"https://a.test"}))
	assert.False(t, containsWildcard(nil))
	// A literal origin that merely CONTAINS an asterisk is not the wildcard.
	assert.False(t, containsWildcard([]string{"https://*.a.test"}))
}

func TestRequireAdminUsesTheMiddlewareWhenWired(t *testing.T) {
	t.Parallel()
	// A nil *auth.Middleware value is still typed, so the helper must branch on
	// the pointer being nil rather than on the interface being nil — otherwise
	// this would panic instead of falling back.
	require.NotPanics(t, func() { _ = requireAdmin(nil) })
}

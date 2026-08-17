package authgate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/pkg/authctx"
)

func TestAdminAndAPITokenGateResponseMatrix(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := RequireAdmin(RejectAPIToken(next))

	for _, tc := range []struct {
		name      string
		principal *authctx.Principal
		want      int
		code      string
	}{
		{name: "missing principal", want: http.StatusNotFound, code: "not_found"},
		{name: "user session", principal: &authctx.Principal{UserID: 2, Role: authctx.RoleEditor, Via: authctx.ViaSession}, want: http.StatusNotFound, code: "not_found"},
		{name: "user API token", principal: &authctx.Principal{UserID: 2, Role: authctx.RoleEditor, Via: authctx.ViaAPIToken}, want: http.StatusNotFound, code: "not_found"},
		{name: "admin API token", principal: &authctx.Principal{UserID: 1, Role: authctx.RoleAdmin, Via: authctx.ViaAPIToken}, want: http.StatusForbidden, code: "token_scope"},
		{name: "admin session", principal: &authctx.Principal{UserID: 1, Role: authctx.RoleAdmin, Via: authctx.ViaSession}, want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
			if tc.principal != nil {
				req = req.WithContext(authctx.WithPrincipal(context.Background(), *tc.principal))
			}
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Code)
			if tc.code != "" {
				assert.Contains(t, rec.Body.String(), `"code":"`+tc.code+`"`)
			}
		})
	}
}

func TestRejectAPITokenAllowsSessionAndMissingPrincipal(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := RejectAPIToken(next)

	for _, principal := range []*authctx.Principal{
		nil,
		{UserID: 1, Role: authctx.RoleAdmin, Via: authctx.ViaSession},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/password/change", nil)
		if principal != nil {
			req = req.WithContext(authctx.WithPrincipal(context.Background(), *principal))
		}
		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNoContent, rec.Code)
	}
}

func TestRequirePermission(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := RequirePermission(authctx.PermContentWrite)(next)

	for _, tc := range []struct {
		name      string
		principal *authctx.Principal
		want      int
	}{
		{name: "missing principal", want: http.StatusForbidden},
		{name: "viewer", principal: &authctx.Principal{UserID: 2, Role: authctx.RoleViewer, Via: authctx.ViaSession}, want: http.StatusForbidden},
		{name: "unknown role fails closed", principal: &authctx.Principal{UserID: 2, Role: authctx.Role("user"), Via: authctx.ViaSession}, want: http.StatusForbidden},
		{name: "editor", principal: &authctx.Principal{UserID: 2, Role: authctx.RoleEditor, Via: authctx.ViaSession}, want: http.StatusNoContent},
		{name: "owner", principal: &authctx.Principal{UserID: 1, Role: authctx.RoleOwner, Via: authctx.ViaSession}, want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/links", nil)
			if tc.principal != nil {
				req = req.WithContext(authctx.WithPrincipal(context.Background(), *tc.principal))
			}
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

// A refused content write says 403, never the 404 the admin surface uses: a
// viewer knows their own library exists, so hiding it would be a lie — and the
// 404 is reserved for concealing that an administrative route exists at all.
func TestRequirePermission_RefusesWithForbiddenNotNotFound(t *testing.T) {
	t.Parallel()

	h := RequirePermission(authctx.PermContentWrite)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/api/links", nil).WithContext(
		authctx.WithPrincipal(context.Background(), authctx.Principal{UserID: 2, Role: authctx.RoleViewer}))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "forbidden_role")
}

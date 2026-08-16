package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSharedSecretGuardDelegatesOnlyExplicitlyAllowedRequests(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	guarded := sharedSecretGuard("topsecret", func(r *http.Request) bool {
		return r.URL.Path == "/api/backup/download" && r.URL.Query().Get("token") == "valid-once"
	})(next)

	for _, tc := range []struct {
		name   string
		url    string
		header string
		want   int
	}{
		{name: "plain request", url: "/api/backup/download", want: http.StatusUnauthorized},
		{name: "unrecognized ticket", url: "/api/backup/download?token=other", want: http.StatusUnauthorized},
		{name: "delegated ticket", url: "/api/backup/download?token=valid-once", want: http.StatusNoContent},
		{name: "ordinary shared secret", url: "/api/tags", header: "topsecret", want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.header != "" {
				req.Header.Set("X-Foldex-Secret", tc.header)
			}
			guarded.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

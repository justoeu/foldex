package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBodyLimitForPath(t *testing.T) {
	assert.Equal(t, int64(maxBodyBackup), bodyLimitForPath("/api/backup"))
	assert.Equal(t, int64(maxBodyBackup), bodyLimitForPath("/api/backup/restore"))
	assert.Equal(t, int64(maxBodyImport), bodyLimitForPath("/api/import"))
	assert.Equal(t, int64(maxBodyImport), bodyLimitForPath("/api/import/apply"))
	assert.Equal(t, int64(maxBodyImage), bodyLimitForPath("/api/links/1/image"))
	assert.Equal(t, int64(maxBodyImage), bodyLimitForPath("/api/notes/images"))
	assert.Equal(t, int64(maxBodyDefault), bodyLimitForPath("/api/links"))
	assert.Equal(t, int64(maxBodyDefault), bodyLimitForPath("/api/tags"))
}

func TestDefaultBodyLimit_RejectsOversizedJSON(t *testing.T) {
	var sawBody bool
	h := defaultBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		sawBody = true
		w.WriteHeader(http.StatusOK)
	}))

	// 1 MiB + 1 exceeds defaultJSON body cap.
	body := strings.Repeat("x", int(maxBodyDefault)+1)
	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.False(t, sawBody, "handler must not fully consume oversized body")
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestDefaultBodyLimit_AllowsSmallBody(t *testing.T) {
	h := defaultBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(`{"url":"https://x"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

package addon_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/addon"
)

func TestBundleBuiltSemantics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		zip     []byte
		version string
	}{
		{name: "placeholder dist", zip: nil, version: "0.0.0"},
		{name: "empty version", zip: []byte("PK"), version: ""},
		{name: "placeholder version with bytes", zip: []byte("PK"), version: "0.0.0"},
	} {
		b := addon.NewBundle(tc.zip, tc.version)
		assert.False(t, b.Built(), tc.name)
		assert.False(t, addon.NewBundle(nil, "\n").Built(), "whitespace-only version")
	}

	b := addon.NewBundle([]byte("PK-zip-bytes"), " 9.9.9\n")
	assert.True(t, b.Built())
	assert.Equal(t, "9.9.9", b.Version(), "version.txt ships with a trailing newline")
	assert.Equal(t, "foldex-extension-9.9.9.zip", b.DownloadName())
}

func TestDownloadServesZipWithSpecHeaders(t *testing.T) {
	zip := []byte("PK\x03\x04 not a real zip, but the exact bytes must round-trip")
	h := addon.NewHandler(addon.NewBundle(zip, "3.1.0"))

	get := httptest.NewRecorder()
	h.Download(get, httptest.NewRequest(http.MethodGet, "/api/admin/addon/download", nil))
	require.Equal(t, http.StatusOK, get.Code, get.Body.String())
	assert.Equal(t, zip, get.Body.Bytes())
	assert.Equal(t, "application/zip", get.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="foldex-extension-3.1.0.zip"`,
		get.Header().Get("Content-Disposition"))
	assert.Equal(t, "3.1.0", get.Header().Get("X-Addon-Version"))

	head := httptest.NewRecorder()
	h.Download(head, httptest.NewRequest(http.MethodHead, "/api/admin/addon/download", nil))
	require.Equal(t, http.StatusOK, head.Code, head.Body.String())
	assert.Empty(t, head.Body.Bytes(), "HEAD answers the same headers and no body")
	assert.Equal(t, "application/zip", head.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="foldex-extension-3.1.0.zip"`,
		head.Header().Get("Content-Disposition"))
	assert.Equal(t, "3.1.0", head.Header().Get("X-Addon-Version"))
	assert.Equal(t, "56", head.Header().Get("Content-Length"))
}

func TestDownloadWithoutABuiltBundleAnswers503(t *testing.T) {
	h := addon.NewHandler(addon.NewBundle(nil, "0.0.0"))

	get := httptest.NewRecorder()
	h.Download(get, httptest.NewRequest(http.MethodGet, "/api/admin/addon/download", nil))
	assert.Equal(t, http.StatusServiceUnavailable, get.Code)
	assert.Contains(t, get.Body.String(), `"code":"addon_not_built"`)

	head := httptest.NewRecorder()
	h.Download(head, httptest.NewRequest(http.MethodHead, "/api/admin/addon/download", nil))
	assert.Equal(t, http.StatusServiceUnavailable, head.Code)
}

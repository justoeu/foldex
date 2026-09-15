package importer

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3776Charter_ValidateLinks(t *testing.T) {
	t.Parallel()
	ok := JSONFile{Links: []JSONLink{{URL: "https://example.com", Title: "ok"}}}
	require.NoError(t, ok.validateLinks())

	cases := []struct {
		name string
		file JSONFile
		want string
	}{
		{
			name: "empty url",
			file: JSONFile{Links: []JSONLink{{URL: "  ", Title: "x"}}},
			want: "links[0]: url is required",
		},
		{
			name: "relative url",
			file: JSONFile{Links: []JSONLink{{URL: "/path", Title: "x"}}},
			want: "links[0]:",
		},
		{
			name: "negative clicks",
			file: JSONFile{Links: []JSONLink{{URL: "https://example.com", ClickCount: -1}}},
			want: "click_count out of range",
		},
		{
			name: "bad created_at",
			file: JSONFile{Links: []JSONLink{{URL: "https://example.com", CreatedAt: "yesterday"}}},
			want: "invalid created_at",
		},
		{
			name: "empty tag name",
			file: JSONFile{Links: []JSONLink{{URL: "https://example.com", Tags: []string{" "}}}},
			want: "links[0].tags[0]: name is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.file.validateLinks()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestS3776Charter_ParseUpload(t *testing.T) {
	h := NewHandler(nil, nil)

	t.Run("missing file", func(t *testing.T) {
		body, ct := multipartBody(t, map[string]string{"format": "netscape"}, "", "", "")
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
		req.Header.Set("Content-Type", ct)
		_, err := h.parseUpload(rec, req)
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "missing_file")
	})

	t.Run("unknown format", func(t *testing.T) {
		body, ct := multipartBody(t, map[string]string{"format": "csv"}, "file", "b.csv", "a,b")
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
		req.Header.Set("Content-Type", ct)
		_, err := h.parseUpload(rec, req)
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "unknown_format")
	})

	t.Run("netscape success", func(t *testing.T) {
		body, ct := multipartBody(t, map[string]string{"format": "netscape"}, "file", "b.html", `<a href="https://example.com">x</a>`)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
		req.Header.Set("Content-Type", ct)
		got, err := h.parseUpload(rec, req)
		require.NoError(t, err)
		assert.Equal(t, "netscape", got.format)
		require.Len(t, got.items, 1)
		assert.Equal(t, "https://example.com", got.items[0].URL)
	})

	t.Run("json validation failed", func(t *testing.T) {
		payload := `{"version":2,"links":[{"url":"not-a-url","title":"x"}]}`
		body, ct := multipartBody(t, map[string]string{"format": "json"}, "file", "b.json", payload)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
		req.Header.Set("Content-Type", ct)
		_, err := h.parseUpload(rec, req)
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "validation_failed")
	})

	t.Run("json parse failed", func(t *testing.T) {
		body, ct := multipartBody(t, map[string]string{"format": "json"}, "file", "b.json", "{")
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/import/validate", body)
		req.Header.Set("Content-Type", ct)
		_, err := h.parseUpload(rec, req)
		require.Error(t, err)
		assert.Contains(t, rec.Body.String(), "parse_failed")
	})
}

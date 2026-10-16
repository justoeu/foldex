package backupstatus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewArtifactClient_HalfConfiguredIsDisabled(t *testing.T) {
	for _, tc := range []struct{ base, token string }{
		{"", ""},
		{"http://agent.internal", ""},
		{"", "t0ken"},
		{"   ", "t0ken"},
		{"http://agent.internal", "  "},
	} {
		assert.Nil(t, NewArtifactClient(tc.base, tc.token), "base=%q token=%q", tc.base, tc.token)
	}
}

func TestNewArtifactClient_TrimsInputsAndTrailingSlashes(t *testing.T) {
	client := NewArtifactClient("  http://agent.internal///  ", "  t0ken ")
	require.NotNil(t, client)
	assert.Equal(t, "http://agent.internal", client.base)
	assert.Equal(t, "t0ken", client.token)
	assert.Equal(t, artifactStreamTimeout, client.http.Timeout)
}

func TestArtifactClient_EnabledIsNilSafe(t *testing.T) {
	var client *ArtifactClient
	assert.False(t, client.Enabled())
	assert.True(t, NewArtifactClient("http://agent.internal", "t0ken").Enabled())
}

func TestArtifactClient_NilClientOpenAndList(t *testing.T) {
	var client *ArtifactClient

	body, err := client.Open(context.Background(), "key", "pass")
	assert.Nil(t, body)
	assert.ErrorIs(t, err, ErrBridgeDisabled)

	listing, err := client.List(context.Background(), "prefix/")
	assert.Zero(t, listing)
	assert.ErrorIs(t, err, ErrBridgeDisabled)
}

func TestArtifactClient_OpenStreamsAndSendsCredentialsInBody(t *testing.T) {
	var gotKey, gotPass, gotAuth, gotContentType, gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		var in map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		gotKey, gotPass = in["key"], in["passphrase"]
		_, _ = io.WriteString(w, "ciphertext-bytes")
	}))
	t.Cleanup(server.Close)

	// A trailing slash on the configured base must not produce a double slash.
	client := NewArtifactClient(server.URL+"/", "t0ken")
	rc, err := client.Open(context.Background(), "dumps/one.age", "a-long-enough-passphrase")
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, "ciphertext-bytes", string(data))

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/internal/artifact", gotPath)
	assert.Equal(t, "Bearer t0ken", gotAuth)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "dumps/one.age", gotKey)
	assert.Equal(t, "a-long-enough-passphrase", gotPass)
}

func TestArtifactClient_OpenNon200IsUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "agent detail that must stay there")
		}))
		client := NewArtifactClient(server.URL, "t0ken")
		body, err := client.Open(context.Background(), "k", "p")
		assert.Nil(t, body, "status %d", status)
		assert.ErrorIs(t, err, ErrArtifactUnavailable, "status %d", status)
		server.Close()
	}
}

func TestArtifactClient_OpenDeadServerAndCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	deadURL := server.URL
	server.Close()

	client := NewArtifactClient(deadURL, "t0ken")
	_, err := client.Open(context.Background(), "k", "p")
	require.ErrorIs(t, err, ErrArtifactUnavailable)
	assert.NotContains(t, err.Error(), deadURL, "transport detail must not leak into the error")

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(live.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewArtifactClient(live.URL, "t0ken").Open(ctx, "k", "p")
	assert.ErrorIs(t, err, ErrArtifactUnavailable)
}

func TestArtifactClient_UnbuildableRequestURL(t *testing.T) {
	client := &ArtifactClient{base: "http://agent\x7f.invalid", token: "t0ken", http: &http.Client{}}

	_, openErr := client.Open(context.Background(), "k", "p")
	require.Error(t, openErr)
	assert.Contains(t, openErr.Error(), "build artifact request")
	assert.NotErrorIs(t, openErr, ErrArtifactUnavailable)

	_, listErr := client.List(context.Background(), "p/")
	require.Error(t, listErr)
	assert.Contains(t, listErr.Error(), "build listing request")
	assert.NotErrorIs(t, listErr, ErrArtifactUnavailable)
}

func TestArtifactClient_ListDecodesListingAndEncodesPrefix(t *testing.T) {
	const prefix = "user zips/a&b=c/"
	var gotPrefix, gotAuth, gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrefix = r.URL.Query().Get("prefix")
		gotAuth, gotPath, gotMethod = r.Header.Get("Authorization"), r.URL.Path, r.Method
		_, _ = io.WriteString(w, `{
			"objects": [
				{"key": "user zips/a&b=c/one.age", "size": 123, "mtime": "2026-01-02T03:04:05Z"},
				{"key": "user zips/a&b=c/two.age", "size": 0, "mtime": ""}
			],
			"capped": true,
			"max_keys": 1000,
			"unknown_field": "ignored"
		}`)
	}))
	t.Cleanup(server.Close)

	listing, err := NewArtifactClient(server.URL, "t0ken").List(context.Background(), prefix)
	require.NoError(t, err)
	assert.Equal(t, ArtifactListing{
		Objects: []ArtifactObject{
			{Key: "user zips/a&b=c/one.age", Size: 123, Mtime: "2026-01-02T03:04:05Z"},
			{Key: "user zips/a&b=c/two.age"},
		},
		Capped:  true,
		MaxKeys: 1000,
	}, listing)
	assert.Equal(t, prefix, gotPrefix, "prefix must survive as one query value, raw & included")
	assert.Equal(t, "Bearer t0ken", gotAuth)
	assert.Equal(t, "/internal/artifacts", gotPath)
	assert.Equal(t, http.MethodGet, gotMethod)
}

func TestArtifactClient_ListEmptyListing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"objects": [], "capped": false, "max_keys": 0}`)
	}))
	t.Cleanup(server.Close)

	listing, err := NewArtifactClient(server.URL, "t0ken").List(context.Background(), "nothing/")
	require.NoError(t, err)
	assert.Empty(t, listing.Objects)
	assert.False(t, listing.Capped)
}

func TestArtifactClient_ListNon200IsUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusBadGateway} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := NewArtifactClient(server.URL, "t0ken").List(context.Background(), "p/")
		assert.ErrorIs(t, err, ErrArtifactUnavailable, "status %d", status)
		server.Close()
	}
}

func TestArtifactClient_ListInvalidBodiesAreUnavailable(t *testing.T) {
	oversized := `{"objects":[{"key":"` + strings.Repeat("a", 1<<20) + `","size":1}]}`
	for name, payload := range map[string]string{
		"not json at all": "the agent fell over",
		"truncated json":  `{"objects": [{"key": "x"`,
		"empty body":      "",
		"wrong types":     `{"objects": "nope"}`,
		"beyond the cap":  oversized,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, payload)
			}))
			t.Cleanup(server.Close)
			_, err := NewArtifactClient(server.URL, "t0ken").List(context.Background(), "p/")
			assert.ErrorIs(t, err, ErrArtifactUnavailable)
			assert.True(t, errors.Is(err, ErrArtifactUnavailable))
		})
	}
}

func TestArtifactClient_ListDeadServerAndCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"objects":[]}`)
	}))
	deadURL := server.URL
	server.Close()

	_, err := NewArtifactClient(deadURL, "t0ken").List(context.Background(), "p/")
	require.ErrorIs(t, err, ErrArtifactUnavailable)
	assert.NotContains(t, err.Error(), deadURL)

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"objects":[]}`)
	}))
	t.Cleanup(live.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewArtifactClient(live.URL, "t0ken").List(ctx, "p/")
	assert.ErrorIs(t, err, ErrArtifactUnavailable)
}

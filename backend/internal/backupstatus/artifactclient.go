package backupstatus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

/*
The backend's half of the download bridge (ADR-48).

Everything this client touches is already ciphertext the caller can open: the
agent decrypts the stored artifact with the instance's private identity and
re-encrypts it to the passphrase the administrator chose, in one pass. So the
web process never holds a backup in the clear, and never holds a bucket
credential — which is as much of INV-171's wall as a download feature can
leave standing.

The client is nil-safe by construction: an unconfigured bridge is not an error
state, it is the DEFAULT, and every caller asks Enabled() first.
*/

// ErrBridgeDisabled means no BACKUP_AGENT_URL/BACKUP_AGENT_TOKEN was configured.
var ErrBridgeDisabled = errors.New("backupstatus: the backup artifact bridge is not configured")

// ErrArtifactUnavailable means the agent could not produce the artifact.
var ErrArtifactUnavailable = errors.New("backupstatus: the agent could not serve the artifact")

// artifactStreamTimeout bounds one download end to end. A dump is minutes of
// streaming on a slow link, and the alternative to a generous ceiling is a
// request that can hang a connection until the process restarts.
const artifactStreamTimeout = 30 * time.Minute

// ArtifactClient talks to the backup agent's internal endpoints.
type ArtifactClient struct {
	base  string
	token string
	http  *http.Client
}

// NewArtifactClient returns a client, or nil when the bridge is not configured.
//
// Both halves are required together: a URL without a token would send the
// passphrase to an endpoint that refuses it, and a token without a URL has
// nowhere to go. Half-configured is treated as OFF rather than as an error,
// because the safe state is the one that changes nothing.
func NewArtifactClient(baseURL, token string) *ArtifactClient {
	baseURL, token = strings.TrimSpace(baseURL), strings.TrimSpace(token)
	if baseURL == "" || token == "" {
		return nil
	}
	return &ArtifactClient{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		// A dedicated client, not http.DefaultClient: the timeout below is
		// this call's, and mutating a shared default would hand it to every
		// other outbound request in the process.
		http: &http.Client{Timeout: artifactStreamTimeout},
	}
}

// Enabled reports whether the bridge exists. Safe on a nil receiver, which is
// exactly how an unconfigured instance carries it.
func (c *ArtifactClient) Enabled() bool { return c != nil }

// Open asks the agent for one artifact, re-encrypted under passphrase.
//
// The returned reader is the caller's to close. The body is streamed, never
// buffered: a dump can be gigabytes and this process has a memory limit.
func (c *ArtifactClient) Open(ctx context.Context, key, passphrase string) (io.ReadCloser, error) {
	if !c.Enabled() {
		return nil, ErrBridgeDisabled
	}
	body, err := json.Marshal(map[string]string{"key": key, "passphrase": passphrase})
	if err != nil {
		return nil, fmt.Errorf("backupstatus: encode artifact request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/internal/artifact", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("backupstatus: build artifact request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// NEVER wrap: a transport error embeds the URL, and this flows to slog.
		// The address is not a secret, but the habit of echoing request detail
		// into logs is how the token ends up there the day someone puts it in
		// a query string.
		return nil, ErrArtifactUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, ErrArtifactUnavailable
	}
	return resp.Body, nil
}

// ArtifactListing is what a prefix holds, as the agent sees it.
type ArtifactListing struct {
	Objects []ArtifactObject `json:"objects"`
	Capped  bool             `json:"capped"`
	MaxKeys int              `json:"max_keys"`
}

// ArtifactObject is one stored object. No checksum and no URL: the agent
// publishes what it can see, and the download goes back through this bridge.
type ArtifactObject struct {
	Key   string `json:"key"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// List enumerates one prefix.
func (c *ArtifactClient) List(ctx context.Context, prefix string) (ArtifactListing, error) {
	if !c.Enabled() {
		return ArtifactListing{}, ErrBridgeDisabled
	}
	// Encoded, not concatenated: today the only caller passes a constant, and
	// the day one does not is the day a raw "&" silently truncates the prefix.
	endpoint := c.base + "/internal/artifacts?" + url.Values{"prefix": {prefix}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ArtifactListing{}, fmt.Errorf("backupstatus: build listing request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return ArtifactListing{}, ErrArtifactUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ArtifactListing{}, ErrArtifactUnavailable
	}
	var out ArtifactListing
	// Capped: the agent bounds its own listing, and this bound is the second
	// half of the same promise — a hostile or broken agent must not be able to
	// make this process read an unbounded body.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return ArtifactListing{}, ErrArtifactUnavailable
	}
	return out, nil
}

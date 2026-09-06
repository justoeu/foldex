package links

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// URLMetadata is the wire shape returned by POST /api/links/url-metadata. Fields
// mirror what the preview worker extracts asynchronously after a link is
// created — exposing them synchronously lets the LinkDialog pre-fill Title /
// Description before the user clicks Save.
type URLMetadata struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	FaviconURL  string `json:"favicon_url"`
	OGImageURL  string `json:"og_image_url"`
}

// MetadataFetcher fetches page metadata for an arbitrary URL. The real
// implementation is *preview.Fetcher (wrapped via an adapter in main.go), which
// dials through the SSRF-guarded transport — IMDS always blocked, RFC1918
// gated by PREVIEW_STRICT_SSRF (CLAUDE.md §4). Kept as an interface so this
// package doesn't import preview and tests can inject a stub.
type MetadataFetcher interface {
	FetchMetadata(ctx context.Context, pageURL string) (URLMetadata, error)
}

type urlMetadataInput struct {
	URL string `json:"url"`
}

// urlMetadataMaxLen caps the accepted url length before we ever hit DNS /
// the SSRF dialer. 2 KiB is well above any real bookmarkable URL — anything
// larger is hostile input we don't want to spend resolver cycles on.
const urlMetadataMaxLen = 2048

// urlMetadataTimeout is the per-request ceiling for metadata resolution.
// HTTP/oEmbed is capped at 5s inside FetchThenRender; Chromium gets 10s.
// 15s stays under the SPA axios default (30s) so the dialog spinner ends first.
const urlMetadataTimeout = 15 * time.Second

// maxMetadataInFlight bounds concurrent url-metadata fetches so a flood cannot
// pin unbounded goroutines on outbound HTTP or the singleton Chromium pool.
const maxMetadataInFlight = 2

const maxMetadataPerUser = 1

// Per-field byte caps applied before returning to the client. The preview
// fetcher only caps the total body at 2 MiB — a single hostile <meta
// content="…1MB description…"> would otherwise round-trip back to the
// dialog (whose `maxLength={1000}` is a keystroke-time clamp; programmatic
// setDescription bypasses it) and then back to the server on Save.
//
// Title mirrors `links.MaxTitleBytes` so a pre-filled title is guaranteed
// to pass the Create/Update DTO. Returning a title longer than that would
// surface as a 400 invalid_input on Save — a self-inflicted UX bug.
//
// Description has no DTO cap today; we use the UI's `maxLength` (1000
// chars) × UTF-8 worst case as the budget so the textarea round-trips
// cleanly. URL fields aren't user input (only the preview worker writes
// them), but the metadata endpoint returns them, so cap as defense in
// depth — 2 KiB matches the input URL cap.
const (
	descByteCap     = 4 << 10
	urlFieldByteCap = 2 << 10
)

// truncateRunes returns s truncated to at most n bytes WITHOUT splitting a
// multi-byte UTF-8 sequence. We walk back to the previous rune boundary if
// the cap falls mid-rune. This keeps the response valid UTF-8 and avoids
// the `replacement-char` cascade that a naive byte slice would produce.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Walk back until we land on a byte that isn't a UTF-8 continuation
	// (top two bits != 10). Worst case: 3 bytes back for a 4-byte rune.
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

func readURLMetadataTarget(w http.ResponseWriter, r *http.Request) (string, error) {
	raw := ""
	if r.Body != nil && r.ContentLength != 0 {
		in, err := httperr.DecodeJSON[urlMetadataInput](w, r)
		if err != nil {
			return "", err
		}
		raw = strings.TrimSpace(in.URL)
	}
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("url"))
	}
	if raw == "" {
		return "", httperr.New(http.StatusBadRequest, "invalid_url", "url is required")
	}
	if len(raw) > urlMetadataMaxLen {
		return "", httperr.New(http.StatusBadRequest, "invalid_url", "url too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", httperr.New(http.StatusBadRequest, "invalid_url", "url must be a valid absolute URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", httperr.New(http.StatusBadRequest, "invalid_scheme", "only http and https are supported")
	}
	return raw, nil
}

func (h *Handler) acquireMetadata(uid authctx.UserID) bool {
	h.metaMu.Lock()
	defer h.metaMu.Unlock()
	if h.metaSem == nil {
		h.metaSem = make(chan struct{}, maxMetadataInFlight)
	}
	if h.metaUsers == nil {
		h.metaUsers = make(map[authctx.UserID]int)
	}
	if h.metaUsers[uid] >= maxMetadataPerUser {
		return false
	}
	select {
	case h.metaSem <- struct{}{}:
		h.metaUsers[uid]++
		return true
	default:
		return false
	}
}

func (h *Handler) releaseMetadata(uid authctx.UserID) {
	h.metaMu.Lock()
	if h.metaUsers[uid] <= 1 {
		delete(h.metaUsers, uid)
	} else {
		h.metaUsers[uid]--
	}
	<-h.metaSem
	h.metaMu.Unlock()
}

func (h *Handler) fetchURLMetadata(w http.ResponseWriter, r *http.Request) {
	// Boot-time guarantee in router.go means this should never be nil at
	// request time, but fail closed if the route ever gets mounted without
	// the dep wired (e.g. test harness) rather than 500-ing inside the call.
	if h.fetcher == nil {
		httperr.Write(w, httperr.New(http.StatusServiceUnavailable, "metadata_unconfigured", "URL metadata fetcher is not configured"))
		return
	}

	raw, err := readURLMetadataTarget(w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	uid := authctx.MustUser(r.Context())
	if !h.acquireMetadata(uid) {
		w.Header().Set("Retry-After", "5")
		httperr.Write(w, httperr.New(http.StatusTooManyRequests, "metadata_busy", "too many URL metadata fetches in flight"))
		return
	}
	defer h.releaseMetadata(uid)

	ctx, cancel := context.WithTimeout(r.Context(), urlMetadataTimeout)
	defer cancel()

	md, err := h.fetcher.FetchMetadata(ctx, raw)
	if err != nil {
		// Third-party fetch failed (timeout, bot wall, SSRF refusal, TLS).
		// That is not an API fault: the request was well-formed and the
		// dialog can still Save. Returning 502 made Grafana's 5xx ratio
		// treat "this page blocked us" as a backend outage. Empty 200
		// lets the SPA fall back to the hostname; the error text never
		// leaves the process (DNS / SSRF / IMDS stay in the log).
		httperr.JSON(w, http.StatusOK, URLMetadata{})
		return
	}
	md.Title = truncateRunes(md.Title, MaxTitleBytes)
	md.Description = truncateRunes(md.Description, descByteCap)
	md.FaviconURL = truncateRunes(md.FaviconURL, urlFieldByteCap)
	md.OGImageURL = truncateRunes(md.OGImageURL, urlFieldByteCap)
	httperr.JSON(w, http.StatusOK, md)
}

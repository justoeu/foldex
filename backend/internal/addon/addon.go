// Package addon serves the Chrome extension bundle that `make extension`
// embeds in the binary, so an operator downloads the addon from their own
// instance instead of a store (SDD R3).
package addon

import (
	_ "embed"
	"net/http"
	"strconv"
	"strings"

	"foldex/internal/pkg/httperr"
)

//go:embed dist/extension.zip
var embeddedZip []byte

//go:embed dist/version.txt
var embeddedVersion string

// notBuiltVersion is what the committed placeholder dist/version.txt carries.
// The placeholder exists so `go build` never fails on the embed for a tree
// where `make extension` was never run.
const notBuiltVersion = "0.0.0"

// Bundle is an immutable view of the embedded artifacts.
type Bundle struct {
	zip     []byte
	version string
}

// NewBundle assembles a Bundle from raw dist artifacts. The version is
// trimmed because version.txt carries a trailing newline.
func NewBundle(zip []byte, version string) Bundle {
	return Bundle{zip: zip, version: strings.TrimSpace(version)}
}

// Embedded returns the bundle compiled into this binary.
func Embedded() Bundle {
	return NewBundle(embeddedZip, embeddedVersion)
}

// Built reports whether a real extension zip is present. An empty zip or the
// placeholder version means the binary was built without `make extension`,
// and the download route must say so rather than serve garbage bytes.
func (b Bundle) Built() bool {
	return len(b.zip) > 0 && b.version != "" && b.version != notBuiltVersion
}

// Version returns the manifest version the zip was built from.
func (b Bundle) Version() string { return b.version }

// DownloadName is the filename the Content-Disposition header carries.
func (b Bundle) DownloadName() string {
	return "foldex-extension-" + b.version + ".zip"
}

// Handler serves the bundle over HTTP.
type Handler struct {
	bundle Bundle
}

// NewHandler wraps a Bundle. Tests inject the not-built placeholder through
// the same constructor the embed path uses.
func NewHandler(b Bundle) *Handler {
	return &Handler{bundle: b}
}

// Download answers GET/HEAD with the zip and its identity headers, or 503
// addon_not_built when this binary carries the placeholder dist.
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	if !h.bundle.Built() {
		httperr.Write(w, httperr.New(http.StatusServiceUnavailable, "addon_not_built",
			"the extension bundle was not embedded in this build — run `make extension` and rebuild"))
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+h.bundle.DownloadName()+`"`)
	w.Header().Set("X-Addon-Version", h.bundle.version)
	if r.Method == http.MethodHead {
		// Explicit Content-Length and no write: net/http drops the body of a
		// HEAD response for us, httptest.ResponseRecorder does not, and a
		// recorder-level test must be able to tell the difference.
		w.Header().Set("Content-Length", strconv.Itoa(len(h.bundle.zip)))
		return
	}
	_, _ = w.Write(h.bundle.zip)
}

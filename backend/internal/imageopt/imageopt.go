// Package imageopt downscales and re-encodes user uploads and screenshots
// before they reach MinIO. Output is always JPEG — lossy is fine because the
// frontend uses these as 150 px thumbnails. Transparency is composited over
// white; animated GIFs collapse to their first frame.
package imageopt

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"

	xdraw "golang.org/x/image/draw"

	// Register decoders so image.Decode dispatches by sniffed format.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	_ "golang.org/x/image/webp"
)

// ErrUnsupportedFormat is returned when the bytes don't sniff to one of the
// four image MIME types Foldex accepts.
var ErrUnsupportedFormat = errors.New("imageopt: unsupported source format")

// ErrDecode wraps decoder errors (truncated/corrupt payloads).
var ErrDecode = errors.New("imageopt: decode failed")

const (
	defaultQuality = 82
	jpegMIME       = "image/jpeg"
	jpegExt        = "jpg"

	// maxPixels caps decoded image area to avoid memory blow-up via decode
	// bombs. A ~50 KB PNG can declare 60000x60000 → image.NewRGBA would
	// allocate ~14 GB and OOM the backend. 50 MP comfortably covers any
	// consumer phone camera (current top is ~108 MP but those compress to
	// 8-15 MB and we cap raw upload bytes upstream).
	maxPixels = 50_000_000
)

// ErrTooLarge is returned when an image's declared pixel area would exceed
// maxPixels. Decoding is rejected BEFORE image.Decode allocates the framebuffer.
var ErrTooLarge = errors.New("imageopt: image dimensions exceed limit")

// supportedInputs is the closed whitelist of MIME types Optimize will accept.
// Mirrors links.allowedUploadMIMEs by design — duplicated so this package
// stays usable without depending on internal/links.
var supportedInputs = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// Options tunes Optimize. Zero values mean "use defaults".
type Options struct {
	MaxDim  int // longest side in pixels; 0 disables resize
	Quality int // JPEG quality 1-100; 0 → defaultQuality
}

// Result is what callers write to storage.
type Result struct {
	Data        []byte
	ContentType string
	Ext         string
	SourceMIME  string
	Resized     bool
	Reencoded   bool
}

// Optimize decodes data, downscales when any side exceeds MaxDim, composites
// transparency over white, and re-encodes to JPEG. If re-encoding would not
// shrink an already-small payload, returns the source bytes untouched.
func Optimize(data []byte, opts Options) (Result, error) {
	if len(data) == 0 {
		return Result{}, fmt.Errorf("%w: empty input", ErrUnsupportedFormat)
	}

	mime := http.DetectContentType(data)
	srcExt, ok := supportedInputs[mime]
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, mime)
	}

	// DecodeConfig reads only the header (a few hundred bytes typically) so we
	// can reject decode bombs without paying the full-buffer allocation. Must
	// happen BEFORE image.Decode — that call commits the framebuffer.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDecode, err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return Result{}, fmt.Errorf("%w: %dx%d (%d pixels) > %d", ErrTooLarge, cfg.Width, cfg.Height, int64(cfg.Width)*int64(cfg.Height), maxPixels)
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDecode, err)
	}

	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()
	dstW, dstH := srcW, srcH
	resized := false
	if opts.MaxDim > 0 && (srcW > opts.MaxDim || srcH > opts.MaxDim) {
		dstW, dstH = scaledDims(srcW, srcH, opts.MaxDim)
		resized = true
	}

	// White-filled RGBA target. JPEG can't carry alpha, so any source
	// transparency must be flattened. draw.Over blends correctly.
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	if resized {
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, srcBounds, xdraw.Over, nil)
	} else {
		draw.Draw(dst, dst.Bounds(), src, srcBounds.Min, draw.Over)
	}

	quality := opts.Quality
	if quality <= 0 {
		quality = defaultQuality
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return Result{}, fmt.Errorf("imageopt: encode jpeg: %w", err)
	}

	// No-regression guard, scoped to JPEG sources only. A small already-tuned
	// JPEG can grow under default settings, in which case we keep the source.
	// PNG/GIF/WebP always re-encode — we want predictable .jpg keys in MinIO
	// even when DEFLATE happens to beat JPEG on a synthetic gradient.
	if mime == jpegMIME && !resized && buf.Len() >= len(data) {
		return Result{
			Data:        data,
			ContentType: mime,
			Ext:         srcExt,
			SourceMIME:  mime,
			Resized:     false,
			Reencoded:   false,
		}, nil
	}

	return Result{
		Data:        buf.Bytes(),
		ContentType: jpegMIME,
		Ext:         jpegExt,
		SourceMIME:  mime,
		Resized:     resized,
		Reencoded:   true,
	}, nil
}

// scaledDims returns the new (w, h) so the longest side equals maxDim and the
// aspect ratio is preserved.
func scaledDims(w, h, maxDim int) (int, int) {
	if w >= h {
		ratio := float64(maxDim) / float64(w)
		nh := int(float64(h) * ratio)
		if nh < 1 {
			nh = 1
		}
		return maxDim, nh
	}
	ratio := float64(maxDim) / float64(h)
	nw := int(float64(w) * ratio)
	if nw < 1 {
		nw = 1
	}
	return nw, maxDim
}

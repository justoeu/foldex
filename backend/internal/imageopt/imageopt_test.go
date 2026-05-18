package imageopt

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptimize_RejectsEmptyInput(t *testing.T) {
	_, err := Optimize(nil, Options{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedFormat)
}

func TestOptimize_RejectsNonImageBytes(t *testing.T) {
	html := []byte("<!doctype html><html><body>nope</body></html>")
	_, err := Optimize(html, Options{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedFormat)
}

func TestOptimize_RejectsCorruptPNG(t *testing.T) {
	// Truncate a valid PNG so the signature passes DetectContentType but the
	// decoder fails partway through.
	full := encodePNG(t, makeGradient(100, 100, false))
	corrupt := full[:len(full)/2]
	_, err := Optimize(corrupt, Options{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDecode)
}

func TestOptimize_PNGDownscaledToCap(t *testing.T) {
	src := encodePNG(t, makeGradient(2000, 1000, false))
	res, err := Optimize(src, Options{MaxDim: 1024, Quality: 82})
	require.NoError(t, err)
	assert.True(t, res.Resized)
	assert.True(t, res.Reencoded)
	assert.Equal(t, "image/jpeg", res.ContentType)
	assert.Equal(t, "jpg", res.Ext)
	assert.Equal(t, "image/png", res.SourceMIME)
	assert.Less(t, len(res.Data), len(src), "downscaled JPEG should be smaller than original PNG")

	cfg := decodeConfig(t, res.Data)
	assert.Equal(t, 1024, cfg.Width)
	assert.Equal(t, 512, cfg.Height)
	assert.Equal(t, "image/jpeg", http.DetectContentType(res.Data))
}

func TestOptimize_PNGTallerThanWide(t *testing.T) {
	src := encodePNG(t, makeGradient(600, 1500, false))
	res, err := Optimize(src, Options{MaxDim: 1024, Quality: 82})
	require.NoError(t, err)
	assert.True(t, res.Resized)
	cfg := decodeConfig(t, res.Data)
	assert.Equal(t, 1024, cfg.Height)
	assert.Equal(t, 409, cfg.Width)
}

func TestOptimize_PNGWithinCapIsNotResized(t *testing.T) {
	src := encodePNG(t, makeGradient(800, 600, false))
	res, err := Optimize(src, Options{MaxDim: 1024, Quality: 82})
	require.NoError(t, err)
	assert.False(t, res.Resized)
	assert.True(t, res.Reencoded, "PNG to JPEG re-encode should run even when no resize")
	cfg := decodeConfig(t, res.Data)
	assert.Equal(t, 800, cfg.Width)
	assert.Equal(t, 600, cfg.Height)
}

func TestOptimize_AlphaCompositedOverWhite(t *testing.T) {
	// Fully transparent 50x50 PNG; expect every pixel to come out white once
	// composited and re-encoded as JPEG.
	img := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	// All zeros = fully transparent black. PNG keeps it; JPEG would render
	// black if alpha wasn't pre-multiplied over white.
	src := encodePNG(t, img)
	res, err := Optimize(src, Options{MaxDim: 1024})
	require.NoError(t, err)
	require.True(t, res.Reencoded)

	out, _, err := image.Decode(bytes.NewReader(res.Data))
	require.NoError(t, err)
	r, g, b, _ := out.At(25, 25).RGBA()
	// JPEG is lossy, allow some headroom around pure white (0xFFFF).
	assert.Greater(t, int(r>>8), 240)
	assert.Greater(t, int(g>>8), 240)
	assert.Greater(t, int(b>>8), 240)
}

func TestOptimize_SmallJPEGIsReturnedUntouched(t *testing.T) {
	// Tiny low-entropy JPEG: re-encoding at q82 should produce >= the source.
	src := encodeJPEG(t, makeGradient(100, 100, false), 60)
	res, err := Optimize(src, Options{MaxDim: 1024, Quality: 82})
	require.NoError(t, err)
	assert.False(t, res.Resized)
	assert.False(t, res.Reencoded, "no-regression guard should keep original bytes")
	assert.Equal(t, "image/jpeg", res.ContentType)
	assert.Equal(t, "jpg", res.Ext)
	assert.Equal(t, src, res.Data, "bytes must be returned verbatim when guard trips")
}

func TestOptimize_LargeJPEGIsResizedAndShrinks(t *testing.T) {
	src := encodeJPEG(t, makeGradient(2400, 1200, false), 95)
	res, err := Optimize(src, Options{MaxDim: 1024, Quality: 82})
	require.NoError(t, err)
	assert.True(t, res.Resized)
	assert.True(t, res.Reencoded)
	assert.Less(t, len(res.Data), len(src))
	cfg := decodeConfig(t, res.Data)
	assert.Equal(t, 1024, cfg.Width)
}

func TestOptimize_StaticGIFConvertsToJPEG(t *testing.T) {
	src := encodeGIF(t, makeGradient(300, 200, false))
	res, err := Optimize(src, Options{MaxDim: 1024, Quality: 82})
	require.NoError(t, err)
	assert.False(t, res.Resized)
	assert.True(t, res.Reencoded)
	assert.Equal(t, "image/jpeg", res.ContentType)
	assert.Equal(t, "image/gif", res.SourceMIME)
}

func TestOptimize_DefaultQualityWhenZero(t *testing.T) {
	src := encodePNG(t, makeGradient(900, 700, false))
	res, err := Optimize(src, Options{MaxDim: 1024})
	require.NoError(t, err)
	require.True(t, res.Reencoded)
	// Sanity: image decodes and dimensions preserved.
	cfg := decodeConfig(t, res.Data)
	assert.Equal(t, 900, cfg.Width)
}

func TestOptimize_LowerQualityProducesSmallerFile(t *testing.T) {
	src := encodePNG(t, makeGradient(900, 700, false))
	hi, err := Optimize(src, Options{MaxDim: 1024, Quality: 90})
	require.NoError(t, err)
	lo, err := Optimize(src, Options{MaxDim: 1024, Quality: 40})
	require.NoError(t, err)
	assert.Less(t, len(lo.Data), len(hi.Data))
}

func TestOptimize_NoMaxDimPreservesDimensions(t *testing.T) {
	src := encodePNG(t, makeGradient(1800, 900, false))
	res, err := Optimize(src, Options{Quality: 82})
	require.NoError(t, err)
	assert.False(t, res.Resized)
	cfg := decodeConfig(t, res.Data)
	assert.Equal(t, 1800, cfg.Width)
	assert.Equal(t, 900, cfg.Height)
}

func TestScaledDims(t *testing.T) {
	tests := []struct {
		w, h, max         int
		wantW, wantH      int
	}{
		{2000, 1000, 1024, 1024, 512},
		{1000, 2000, 1024, 512, 1024},
		{1024, 768, 1024, 1024, 768},
		{50, 30, 100, 100, 60},
	}
	for _, tt := range tests {
		gotW, gotH := scaledDims(tt.w, tt.h, tt.max)
		assert.Equal(t, tt.wantW, gotW)
		assert.Equal(t, tt.wantH, gotH)
	}
}

func TestErrors_AreDistinct(t *testing.T) {
	assert.False(t, errors.Is(ErrDecode, ErrUnsupportedFormat))
	assert.False(t, errors.Is(ErrUnsupportedFormat, ErrDecode))
}

// --- helpers ---

func makeGradient(w, h int, alpha bool) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha {
				a = uint8((x * 255) / w)
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8((x * 255) / w),
				G: uint8((y * 255) / h),
				B: uint8(((x + y) * 255) / (w + h)),
				A: a,
			})
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}))
	return buf.Bytes()
}

func encodeGIF(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, gif.Encode(&buf, img, nil))
	return buf.Bytes()
}

func decodeConfig(t *testing.T, data []byte) image.Config {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	return cfg
}

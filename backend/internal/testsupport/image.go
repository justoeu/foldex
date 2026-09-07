// Package testsupport holds test fixtures shared across packages: decode-bomb
// images and production-source AST walkers. It exists so a fix to a fixture
// (e.g. a decoder quirk in the image library, or skipping generated files in
// the walker) cannot silently miss the other packages that need it.
//
// Test-only: production code must not import this package.
package testsupport

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

// PNG encodes a solid-color w×h PNG.
func PNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// BombPNG is a tiny PNG whose IHDR declares 8000×8000 (64 MP, over the 50 MP
// decode cap) while the IDAT stays a 1×1 pixel. Decoding it naively would let
// any decoder OOM; the decode-cap guards must refuse before image.Decode
// allocates.
func BombPNG(t *testing.T) []byte {
	t.Helper()
	bomb := append([]byte(nil), PNG(t, 1, 1)...)
	require.GreaterOrEqual(t, len(bomb), 33)
	binary.BigEndian.PutUint32(bomb[16:20], 8_000)
	binary.BigEndian.PutUint32(bomb[20:24], 8_000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	return bomb
}

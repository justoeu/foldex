package testsupport

import (
	"encoding/binary"
	"testing"
)

func TestBombPNGDeclaresBombDimensions(t *testing.T) {
	bomb := BombPNG(t)
	if w := binary.BigEndian.Uint32(bomb[16:20]); w != 8_000 {
		t.Fatalf("IHDR width = %d, want 8000", w)
	}
	if h := binary.BigEndian.Uint32(bomb[20:24]); h != 8_000 {
		t.Fatalf("IHDR height = %d, want 8000", h)
	}
}

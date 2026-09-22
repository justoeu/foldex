package appsetting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDecodeJSONOrFallback_KeepsDefaultsOnGarbage(t *testing.T) {
	type doc struct {
		N int `json:"n"`
	}
	fallback := doc{N: 7}
	assert.Equal(t, fallback, decodeJSONOrFallback([]byte("not-json"), fallback))
	assert.Equal(t, doc{N: 3}, decodeJSONOrFallback([]byte(`{"n":3}`), fallback))
}

func TestDecodeJSONOrFallback_OverlaysIntoFallback(t *testing.T) {
	type doc struct {
		N int `json:"n"`
		M int `json:"m"`
	}
	got := decodeJSONOrFallback([]byte(`{"n":3}`), doc{N: 7, M: 9})
	assert.Equal(t, doc{N: 3, M: 9}, got)
}

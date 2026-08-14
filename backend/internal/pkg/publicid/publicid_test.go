package publicid

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
		ok   bool
	}{
		{name: "positive", raw: "42", want: 42, ok: true},
		{name: "max int64", raw: "9223372036854775807", want: math.MaxInt64, ok: true},
		{name: "max int64 overflow", raw: "9223372036854775808"},
		{name: "wrapping overflow", raw: "18446744073709551617"},
		{name: "zero", raw: "0"},
		{name: "positive sign", raw: "+42"},
		{name: "negative sign", raw: "-42"},
		{name: "numeric prefix slug", raw: "42-notes"},
		{name: "numeric suffix slug", raw: "notes-42"},
		{name: "empty", raw: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.raw)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

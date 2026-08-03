package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/notes"
)

func TestMapOptionalID(t *testing.T) {
	m := map[int64]int64{1: 10, 2: 20}

	assert.Nil(t, mapOptionalID(m, nil))

	old := int64(1)
	got := mapOptionalID(m, &old)
	requireNotNil := func(p *int64) int64 {
		t.Helper()
		if p == nil {
			t.Fatal("nil")
		}
		return *p
	}
	assert.Equal(t, int64(10), requireNotNil(got))

	missing := int64(99)
	assert.Nil(t, mapOptionalID(m, &missing))
}

func TestSanitizeNoteBody(t *testing.T) {
	html, text := notes.SanitizeBody(`<p>hi<script>alert(1)</script></p>`)
	assert.NotContains(t, html, "<script>")
	assert.Contains(t, text, "hi")

	html, text = notes.SanitizeBody("")
	assert.Equal(t, "", html)
	assert.Equal(t, "", text)
}

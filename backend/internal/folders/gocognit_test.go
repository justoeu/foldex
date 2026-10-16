package folders

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/testsupport"
)

func TestCognitiveComplexity_ListUpdateDeleteCascadeValidate(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{file: "repository.go", name: "(*Repository).List"},
		{file: "repository.go", name: "(*Repository).Update"},
		{file: "repository.go", name: "(*Repository).deleteCascade"},
		{file: "dto.go", name: "(UpdateInput).Validate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testsupport.CognitOf(t, tc.file, tc.name)
			assert.LessOrEqual(t, got, 15,
				"%s cognitive=%d — extract early returns / unexported helpers, no NOSONAR", tc.name, got)
		})
	}
}

package links

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The scheme policy is shared by the CRUD DTOs and the importer; this table
// is the single source for what the rule accepts.
func TestValidateAbsoluteHTTPURL(t *testing.T) {
	assert.NoError(t, ValidateAbsoluteHTTPURL("https://example.com/a?b=c"))
	assert.NoError(t, ValidateAbsoluteHTTPURL("http://localhost:9089"))
	assert.NoError(t, ValidateAbsoluteHTTPURL("HTTPS://EXAMPLE.com"))
	assert.NoError(t, ValidateAbsoluteHTTPURL("  https://example.com  "))

	for raw, want := range map[string]string{
		"/relative/path":      "absolute http(s) URL",
		"example.com/x":       "absolute http(s) URL",
		"ftp://example.com":   "scheme must be http",
		"file:///etc/passwd":  "absolute http(s) URL",
		"javascript:alert(1)": "absolute http(s) URL",
		"http://%zz":          "absolute http(s) URL",
		"https:":              "absolute http(s) URL",
	} {
		err := ValidateAbsoluteHTTPURL(raw)
		assert.ErrorContains(t, err, want, "raw=%q", raw)
	}
}

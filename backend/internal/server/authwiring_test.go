package server

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/config"
)

func TestNewPanicsWhenAuthEnabledWithoutMiddleware(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	assert.PanicsWithValue(t,
		"server: AUTH_ENABLED requires AuthMiddleware",
		func() {
			_ = New(Deps{
				Logger: logger,
				Config: config.Config{AuthEnabled: true},
			})
		})
}

// The Fetch spec forbids `Access-Control-Allow-Origin: *` together with
// credentialed requests, and browsers reject the preflight rather than
// explaining why. Since PR2 the session lives in cookies, so a wildcard origin
// list has to be replaced at boot instead of silently breaking every
// cross-origin call.
func TestContainsWildcard(t *testing.T) {
	t.Parallel()
	assert.True(t, containsWildcard([]string{"*"}))
	assert.True(t, containsWildcard([]string{"https://a.test", "*"}))
	assert.False(t, containsWildcard([]string{"https://a.test"}))
	assert.False(t, containsWildcard(nil))
	// A literal origin that merely CONTAINS an asterisk is not the wildcard.
	assert.False(t, containsWildcard([]string{"https://*.a.test"}))
}

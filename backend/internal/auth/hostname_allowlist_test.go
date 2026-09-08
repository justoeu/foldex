package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"foldex/internal/policy"
)

func TestHostnameAllowlist_ExampleCOMAcceptedOnBothPathsAfterFold(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateEmail("user@Example.COM"),
		"email path must accept Example.COM after folding the domain")

	p := policy.Default()
	p.GoogleAllowedDomains = []string{"Example.COM"}
	require.NoError(t, p.Validate(),
		"policy path must accept Example.COM after folding the domain")
}

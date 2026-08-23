package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The validator gates registration, invitations, administrator-created accounts
// AND the e-mail change, and it had no unit test of its own — the only coverage
// was one obviously-not-an-address string in an integration test.
func TestValidateEmail(t *testing.T) {
	t.Parallel()

	t.Run("accepts addresses people actually have", func(t *testing.T) {
		for _, ok := range []string{
			"a@b.co",
			"jane.smith@company.com",
			"jane+foldex@gmail.com",
			"first.last@sub.domain.co.uk",
			"weird!#$%&'*+-=?^_`{|}~name@example.com",
			"x@example.a",
		} {
			assert.NoErrorf(t, validateEmail(ok), "rejected %q", ok)
		}
	})

	t.Run("refuses what is not an address", func(t *testing.T) {
		for _, bad := range []string{
			"", "no-at-sign", "@example.com", "a@", "a@b", "a@b.", "a@.com",
			"two@at@example.com", "spaces in@example.com", "a\tb@example.com",
			".leading@example.com", "trailing.@example.com", "double..dot@example.com",
		} {
			assert.Errorf(t, validateEmail(bad), "accepted %q", bad)
		}
	})

	// The refuted claim from the adversarial review: an unchecked local part let
	// a URL be a valid "address", and that string is echoed into the linkless
	// notice sent to the address being moved AWAY from. Mail clients linkify a
	// bare https://, so the one message that promises it carries no link would
	// have delivered one — to the person being attacked, from us.
	t.Run("refuses a URL wearing an address as a suffix", func(t *testing.T) {
		for _, attack := range []string{
			"https://evil.tld/verify?redirect=@ok.com",
			"http://evil.tld@ok.com",
			"//evil.tld@ok.com",
			"click:here@ok.com",
		} {
			require.Errorf(t, validateEmail(attack), "accepted %q", attack)
		}
	})
}

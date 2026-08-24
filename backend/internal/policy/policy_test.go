package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/policy"
)

func TestDefault_IsTheHistoricalBehaviour(t *testing.T) {
	d := policy.Default()
	assert.Equal(t, policy.MinPasswordFloor, d.PasswordMinLength)
	assert.False(t, d.GoogleAutoProvision, "auto-provisioning must be off until an owner turns it on")
	assert.Empty(t, d.GoogleAllowedDomains)
	assert.Equal(t, authctx.RoleEditor, d.GoogleDefaultRole)
	require.NoError(t, d.Validate())
}

// The floors are the whole point of the type: a policy screen that could set
// the minimum password length to 1 would be a way to lower the instance's
// security from inside the instance.
func TestValidate_RefusesEverythingBelowTheFloors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*policy.Policy)
	}{
		{"password below floor", func(p *policy.Policy) { p.PasswordMinLength = policy.MinPasswordFloor - 1 }},
		// Not MaxPasswordFloor+1: that bound now belongs to ValidateForWrite,
		// because Get falls back to Default() on a failed Validate and a stored
		// document must not lose its Google allowlist to a tightened password
		// rule. See TestPasswordFloor_WriteBoundTightensAndReadBoundDoesNot.
		{"password absurdly high", func(p *policy.Policy) { p.PasswordMinLength = 10_000 }},
		{"otp ttl zero", func(p *policy.Policy) { p.OTPTTLMinutes = 0 }},
		{"otp ttl too long", func(p *policy.Policy) { p.OTPTTLMinutes = policy.MaxOTPTTLMinutes + 1 }},
		{"cooldown too short", func(p *policy.Policy) { p.OTPCooldownSeconds = policy.MinOTPCooldownSecs - 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policy.Default()
			tc.mutate(&p)
			assert.Error(t, p.Validate())
		})
	}
}

// Auto-provisioning is the one setting here that WIDENS access. Without a
// domain list it would let anyone holding any Google account create themselves
// a tenant on the instance.
func TestValidate_AutoProvisionRequiresAnAllowlist(t *testing.T) {
	p := policy.Default()
	p.GoogleAutoProvision = true
	require.Error(t, p.Validate(), "an empty allowlist plus auto-provisioning is an open instance")

	p.GoogleAllowedDomains = []string{"example.com"}
	assert.NoError(t, p.Validate())
}

// A self-service signup must never arrive holding administration.
func TestValidate_AutoProvisionedRoleIsNeverAdministrative(t *testing.T) {
	for _, role := range []authctx.Role{authctx.RoleOwner, authctx.RoleAdmin, authctx.Role("root"), ""} {
		p := policy.Default()
		p.GoogleDefaultRole = role
		assert.Error(t, p.Validate(), "role %q must be refused as a default", role)
	}
	for _, role := range []authctx.Role{authctx.RoleEditor, authctx.RoleViewer} {
		p := policy.Default()
		p.GoogleDefaultRole = role
		assert.NoError(t, p.Validate(), "role %q must be allowed", role)
	}
}

func TestValidate_RejectsMalformedDomains(t *testing.T) {
	for _, d := range []string{
		"", "example", ".example.com", "example.com.", "http://example.com",
		"user@example.com", "example.com/path", "*.example.com", "Example.com",
		"exa mple.com", "example..com",
		// The blocklist this replaced ("/@ *:") let control characters through,
		// so a domain could carry a newline all the way into an error message
		// and from there into a log line, where it forges a whole record.
		"a.b\nFAKE LOG ENTRY", "a.b\rinjected", "a.b\ttab",
		// Non-ASCII must be punycode before it gets here; accepting raw UTF-8
		// would make two visually identical allowlists behave differently.
		"exámple.com", "example.com\u0000",
		// RFC 1035: a label may not start or end with a hyphen.
		"-example.com", "example-.com", "a.-b.com",
	} {
		p := policy.Default()
		p.GoogleAllowedDomains = []string{d}
		assert.Error(t, p.Validate(), "domain %q must be refused", d)
	}
}

// The comparison is on the exact domain. A suffix match would also accept
// "notexample.com" for an allowlist naming "example.com".
func TestAllowsEmail_MatchesTheExactDomainOnly(t *testing.T) {
	p := policy.Default()
	p.GoogleAllowedDomains = []string{"example.com"}

	assert.True(t, p.AllowsEmail("ana@example.com"))
	assert.True(t, p.AllowsEmail("ANA@EXAMPLE.COM"), "the domain comparison is case-insensitive")
	assert.False(t, p.AllowsEmail("ana@notexample.com"))
	assert.False(t, p.AllowsEmail("ana@example.com.evil.test"))
	assert.False(t, p.AllowsEmail("ana@sub.example.com"), "a subdomain is not the domain")
	assert.False(t, p.AllowsEmail("no-at-sign"))
}

// An empty list narrows nothing: it is the historical behaviour, where the
// subject still has to match an existing account.
func TestAllowsEmail_EmptyListAllowsEverything(t *testing.T) {
	p := policy.Default()
	assert.True(t, p.AllowsEmail("anyone@anywhere.test"))
}

// The write bound is bcrypt's truncation point, and the read bound is NOT.
//
// Tightening both would make an instance configured at 100 fall back to
// Default() on every read — floor 8, empty Google allowlist, default OTP
// lifetime — so a rule getting stricter would be the thing that switched the
// other rules off.
func TestPasswordFloor_WriteBoundTightensAndReadBoundDoesNot(t *testing.T) {
	beyondBcrypt := policy.Default()
	beyondBcrypt.PasswordMinLength = policy.MaxPasswordFloor + 1

	require.Error(t, beyondBcrypt.ValidateForWrite(),
		"an owner must not be able to SAVE a floor no password can satisfy")
	require.NoError(t, beyondBcrypt.Validate(),
		"a document already stored at that floor must still be honoured, "+
			"or tightening this rule silently reverts every other one")

	atBound := policy.Default()
	atBound.PasswordMinLength = policy.MaxPasswordFloor
	require.NoError(t, atBound.ValidateForWrite(), "the bound itself is settable")
}

func TestPasswordFloor_WriteBoundStillRefusesBelowTheFloor(t *testing.T) {
	tooLow := policy.Default()
	tooLow.PasswordMinLength = policy.MinPasswordFloor - 1
	assert.Error(t, tooLow.ValidateForWrite())
}

// The historical ceiling is what a stored document may carry, and it is still
// finite: a value past it is a corrupt row, not a configuration.
func TestPasswordFloor_ReadBoundIsNotUnbounded(t *testing.T) {
	absurd := policy.Default()
	absurd.PasswordMinLength = 10_000
	assert.Error(t, absurd.Validate())
}

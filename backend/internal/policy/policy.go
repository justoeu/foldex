// Package policy holds the instance-wide rules an owner may adjust: how long a
// password has to be, how long a mailed code lives, and which Google accounts
// may sign in — ADR-35.
//
// It is a leaf on purpose. internal/auth ENFORCES these values, so auth imports
// policy and policy imports nothing of auth; putting the settings inside auth
// instead would have made the administration handler that edits them import the
// whole identity surface.
//
// Every value has a FLOOR that configuration cannot cross. A policy screen that
// could set the minimum password length to 1, or hand new Google accounts the
// admin role, would be a way to lower the instance's security from inside the
// instance — which is also why writing these requires the owner rather than any
// administrator.
package policy

import (
	"fmt"
	"strings"

	"foldex/internal/pkg/authctx"
)

// Bounds. The lower ends are the values the code used before any of this was
// configurable, so an instance that never opens the screen behaves exactly as
// it did — and one that does open it cannot end up weaker than that baseline.
const (
	MinPasswordFloor   = 8
	MaxPasswordFloor   = 128
	MinOTPTTLMinutes   = 1
	MaxOTPTTLMinutes   = 30
	MinOTPCooldownSecs = 30
	MaxOTPCooldownSecs = 600
	MaxAllowedDomains  = 32
	maxDomainLen       = 253
)

// Policy is the whole editable surface, and the shape the API returns.
type Policy struct {
	PasswordMinLength  int `json:"password_min_length"`
	OTPTTLMinutes      int `json:"otp_ttl_minutes"`
	OTPCooldownSeconds int `json:"otp_cooldown_seconds"`

	// GoogleAllowedDomains restricts which mailbox domains may sign in through
	// Google. Empty means "no domain restriction" — which, with auto-provision
	// off, is the historical behaviour: the subject still has to match an
	// existing account.
	GoogleAllowedDomains []string `json:"google_allowed_domains"`
	// GoogleAutoProvision creates an account for a Google user who has none.
	//
	// OFF by default, and it is the one setting here that widens rather than
	// narrows: it revokes the invite-only rule ADR-31 established. It is
	// therefore refused unless GoogleAllowedDomains is non-empty — an open
	// allowlist plus auto-provisioning would let anyone with any Google account
	// create themselves a tenant on the instance.
	GoogleAutoProvision bool `json:"google_auto_provision"`
	// GoogleDefaultRole is what an auto-provisioned account receives. Never
	// owner and never admin: a self-service signup must not be able to arrive
	// holding administration.
	GoogleDefaultRole authctx.Role `json:"google_default_role"`
}

// Default is what an instance that never opened the screen runs under.
func Default() Policy {
	return Policy{
		PasswordMinLength:    MinPasswordFloor,
		OTPTTLMinutes:        5,
		OTPCooldownSeconds:   60,
		GoogleAllowedDomains: []string{},
		GoogleAutoProvision:  false,
		GoogleDefaultRole:    authctx.RoleEditor,
	}
}

// Validate clamps nothing and rejects everything out of range.
//
// Rejecting rather than silently clamping: an owner who types 4 and is shown 8
// has learned the floor, while one whose 4 is quietly stored as 8 believes the
// instance is configured a way it is not.
func (p Policy) Validate() error {
	if p.PasswordMinLength < MinPasswordFloor || p.PasswordMinLength > MaxPasswordFloor {
		return fmt.Errorf("password_min_length must be between %d and %d",
			MinPasswordFloor, MaxPasswordFloor)
	}
	if p.OTPTTLMinutes < MinOTPTTLMinutes || p.OTPTTLMinutes > MaxOTPTTLMinutes {
		return fmt.Errorf("otp_ttl_minutes must be between %d and %d",
			MinOTPTTLMinutes, MaxOTPTTLMinutes)
	}
	if p.OTPCooldownSeconds < MinOTPCooldownSecs || p.OTPCooldownSeconds > MaxOTPCooldownSecs {
		return fmt.Errorf("otp_cooldown_seconds must be between %d and %d",
			MinOTPCooldownSecs, MaxOTPCooldownSecs)
	}
	if len(p.GoogleAllowedDomains) > MaxAllowedDomains {
		return fmt.Errorf("at most %d allowed domains", MaxAllowedDomains)
	}
	for _, d := range p.GoogleAllowedDomains {
		if !validDomain(d) {
			return fmt.Errorf("invalid domain %q", d)
		}
	}
	if p.GoogleAutoProvision && len(p.GoogleAllowedDomains) == 0 {
		return fmt.Errorf("auto-provisioning requires at least one allowed domain")
	}
	if p.GoogleDefaultRole != authctx.RoleEditor && p.GoogleDefaultRole != authctx.RoleViewer {
		return fmt.Errorf("google_default_role must be editor or viewer")
	}
	return nil
}

// validDomain accepts a bare registrable name — no scheme, no path, no wildcard.
//
// A wildcard is refused rather than interpreted: "*.example.com" reads as if it
// excluded example.com itself, and guessing which the owner meant is how an
// allowlist ends up wider than it looks.
func validDomain(d string) bool {
	if d == "" || len(d) > maxDomainLen {
		return false
	}
	// An ALLOWLIST of characters, not a blocklist of the ones that looked
	// dangerous. The blocklist this replaced ("/@ *:") let a newline through, so
	// "a.b\nFAKE ENTRY" validated — and that string reaches an error message,
	// which reaches a log line, where an embedded newline forges a whole log
	// record. Enumerating what a hostname may contain closes that and every
	// variant of it at once.
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.':
		default:
			return false
		}
	}
	if strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") || !strings.Contains(d, ".") {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		// A label may not be empty, nor start or end with a hyphen (RFC 1035).
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}

// AllowsEmail reports whether an address may sign in through Google under this
// policy.
//
// An empty allowlist allows everything: the list narrows Google sign-in, it
// does not enable it. Whether an account exists at all is a separate question
// that internal/auth still answers.
func (p Policy) AllowsEmail(email string) bool {
	if len(p.GoogleAllowedDomains) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	// Compared on the exact domain, never as a suffix: a suffix match on
	// "example.com" would also accept "notexample.com".
	domain := strings.ToLower(email[at+1:])
	for _, d := range p.GoogleAllowedDomains {
		if domain == d {
			return true
		}
	}
	return false
}

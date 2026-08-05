package auth

import (
	"strings"
	"time"

	"foldex/internal/pkg/authctx"
)

// Account status values, mirroring app_user_status_check.
const (
	StatusPending  = "pending"
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Revocation reasons, mirroring session_revoked_reason_check.
const (
	ReasonLogout          = "logout"
	ReasonLogoutAll       = "logout_all"
	ReasonReuseDetected   = "reuse_detected"
	ReasonPasswordChanged = "password_changed"
	ReasonAdminRevoked    = "admin_revoked"
	ReasonUserDisabled    = "user_disabled"
)

// User is one row of app_user, minus the secrets.
//
// PasswordHash is deliberately NOT here. It is loaded by the single repository
// method that verifies a password and never travels further, so no handler can
// accidentally serialize it — the type system does the work that a `json:"-"`
// tag only asks politely for.
type User struct {
	ID              authctx.UserID `json:"id"`
	Email           string         `json:"email"`
	Name            string         `json:"name"`
	Role            authctx.Role   `json:"role"`
	Status          string         `json:"status"`
	EmailVerifiedAt *time.Time     `json:"email_verified_at,omitempty"`
	LastLoginAt     *time.Time     `json:"last_login_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	// HasPassword is false for an account that has not been claimed yet, and
	// (from PR4) for one converted to Google-only.
	HasPassword bool `json:"has_password"`
	// TOTPEnabled is true when the account has a CONFIRMED authenticator. An
	// enrollment that was started and abandoned does not count — it would make
	// the UI claim protection the user cannot actually satisfy.
	TOTPEnabled bool `json:"totp_enabled"`
}

// SessionInfo is one live session as shown on the "your devices" list.
type SessionInfo struct {
	ID         int64     `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	UserAgent  string    `json:"user_agent,omitempty"`
	IP         string    `json:"ip,omitempty"`
	Current    bool      `json:"current"`
}

// Invite is one row of invite, as the admin screen sees it.
type Invite struct {
	ID         int64        `json:"id"`
	Email      string       `json:"email"`
	Role       authctx.Role `json:"role"`
	CreatedAt  time.Time    `json:"created_at"`
	ExpiresAt  time.Time    `json:"expires_at"`
	AcceptedAt *time.Time   `json:"accepted_at,omitempty"`
	// AcceptURL is populated ONLY in the response that creates the invite —
	// it embeds the raw token, which the server cannot reproduce afterwards.
	// The admin screen shows it once so an operator whose SMTP is not
	// configured can still deliver the invitation by hand.
	AcceptURL string `json:"accept_url,omitempty"`
}

// issuedTokens carries the raw credentials out of the session layer so the
// handler can set them as cookies. They are returned as a value, never stored:
// what reaches the database is their sha256.
type issuedTokens struct {
	Access        string
	Refresh       string
	CSRF          string
	AccessExpiry  time.Time
	RefreshExpiry time.Time
}

// NormalizeEmail is the ONE normalization every path must use: the login
// lookup, the invite match, and (from PR4) the OAuth linking rule.
//
// It is duplicated as a CHECK constraint on app_user (email_normalized =
// lower(btrim(email))) so the database refuses a row this function did not
// produce. That redundancy is the point — a drift between Go and SQL here is
// not a search bug, it is two accounts that should have been one, or a login
// that matches the wrong row.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// MaskEmail renders an address for a response that must not confirm the full
// address to whoever triggered it (the 2FA-pending payload in PR3).
func MaskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "•••"
	}
	local, domain := email[:at], email[at+1:]
	switch {
	case len(local) <= 1:
		local = "•"
	case len(local) == 2:
		local = local[:1] + "•"
	default:
		local = local[:2] + strings.Repeat("•", min(len(local)-2, 6))
	}
	return local + "@" + domain
}

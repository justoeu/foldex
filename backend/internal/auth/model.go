package auth

import (
	"errors"
	"regexp"
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
	// ReasonEmailChanged is its own reason and not `password_changed`: the
	// login identifier moved, which is a different sentence for the person
	// reading the audit trail a month later.
	ReasonEmailChanged = "email_changed"
)

// authStatus is the closed status domain emitted by authentication responses.
type authStatus string

const (
	statusAnonymous              authStatus = "anonymous"
	statusSetupRequired          authStatus = "setup_required"
	statusAuthenticated          authStatus = "authenticated"
	statusTwoFactorRequired      authStatus = "two_factor_required"
	statusConvertPasswordAccount authStatus = "convert_password_account"
)

// AuthFeatures describes optional authentication capabilities exposed to the
// client. Keeping this typed prevents a misspelled feature key from silently
// disappearing from every auth-state response.
type AuthFeatures struct {
	GoogleOAuth   bool `json:"google_oauth"`
	TwoFactor     bool `json:"two_factor"`
	EmailDelivery bool `json:"email_delivery"`
}

// authWireResponse is the closed set of state-bearing auth responses. Each
// variant has the fields required by its status instead of one map that admits
// impossible combinations.
type authWireResponse interface {
	authWireResponse()
}

type anonymousAuthResponse struct {
	Status   authStatus   `json:"status"`
	Features AuthFeatures `json:"features"`
}

func (anonymousAuthResponse) authWireResponse() {}

type setupRequiredAuthResponse struct {
	Status   authStatus   `json:"status"`
	Features AuthFeatures `json:"features"`
}

func (setupRequiredAuthResponse) authWireResponse() {}

type authenticatedAuthResponse struct {
	Status        authStatus           `json:"status"`
	User          User                 `json:"user"`
	CSRFToken     string               `json:"csrf_token"`
	Features      AuthFeatures         `json:"features"`
	Permissions   []authctx.Permission `json:"permissions"`
	RecoveryCodes []string             `json:"recovery_codes,omitempty"`
}

func (authenticatedAuthResponse) authWireResponse() {}

type twoFactorAuthResponse struct {
	Status      authStatus       `json:"status"`
	Purpose     ChallengePurpose `json:"purpose"`
	Email       string           `json:"email"`
	Methods     []string         `json:"methods"`
	ExpiresIn   int              `json:"expires_in"`
	MaxAttempts int              `json:"max_attempts"`
	Features    AuthFeatures     `json:"features"`
}

func (twoFactorAuthResponse) authWireResponse() {}

type enrollmentAuthResponse struct {
	Status      authStatus       `json:"status"`
	Purpose     ChallengePurpose `json:"purpose"`
	Email       string           `json:"email"`
	Methods     []string         `json:"methods"`
	ExpiresIn   int              `json:"expires_in"`
	MaxAttempts int              `json:"max_attempts"`
	Features    AuthFeatures     `json:"features"`
	Reason      string           `json:"reason"`
}

func (enrollmentAuthResponse) authWireResponse() {}

type conversionAuthResponse struct {
	Status      authStatus       `json:"status"`
	Purpose     ChallengePurpose `json:"purpose"`
	Email       string           `json:"email"`
	Methods     []string         `json:"methods"`
	ExpiresIn   int              `json:"expires_in"`
	MaxAttempts int              `json:"max_attempts"`
	Features    AuthFeatures     `json:"features"`
}

func (conversionAuthResponse) authWireResponse() {}

// User is one row of app_user, minus the secrets.
//
// PasswordHash is deliberately NOT here. It is loaded by the single repository
// method that verifies a password and never travels further, so no handler can
// accidentally serialize it — the type system does the work that a `json:"-"`
// tag only asks politely for.
type User struct {
	ID    authctx.UserID `json:"id"`
	Email string         `json:"email"`
	// Username is the optional second way in. Empty means the account has none
	// and signs in by address only — it is NOT a display field and never
	// appears on anything a stranger can read.
	Username        string       `json:"username"`
	Name            string       `json:"name"`
	Role            authctx.Role `json:"role"`
	Status          string       `json:"status"`
	EmailVerifiedAt *time.Time   `json:"email_verified_at,omitempty"`
	LastLoginAt     *time.Time   `json:"last_login_at,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	// HasPassword is false for an account that has not been claimed yet, and
	// for one converted to Google-only.
	HasPassword bool `json:"has_password"`
	// TOTPEnabled is true when the account has a CONFIRMED authenticator. An
	// enrollment that was started and abandoned does not count — it would make
	// the UI claim protection the user cannot actually satisfy.
	TOTPEnabled bool `json:"totp_enabled"`
	// Email2FAEnabled is the same statement for a CONFIRMED e-mail factor
	// (ADR-37). Derived with EXISTS on every read for the same reason
	// TOTPEnabled is: a cached boolean needs updating in four places, and the
	// first one missed decides whether login demands a code nobody can produce.
	Email2FAEnabled bool `json:"email_2fa_enabled"`
	// Locale is the account's preferred language for e-mail, chosen in the
	// profile. Empty means "no preference", which is NOT the same as English:
	// without one, a message falls back to the Accept-Language of whoever
	// triggered it, and only then to the default.
	Locale string `json:"locale,omitempty"`
	// TokenVersion is the credential epoch used only at repository boundaries.
	// Keeping it off the wire prevents clients from treating it as a claim.
	TokenVersion int `json:"-"`
}

// HasSecondFactor reports whether this account can satisfy a second factor at
// all, by any method.
//
// A METHOD, not a boolean column, and not a third derived field: every place
// that used to ask "does this account have TOTP" was really asking this, and
// leaving those call sites reading TOTPEnabled is how e-mail-only accounts
// would silently be treated as having no factor — diverted into mandatory
// enrollment they already completed, or refused an administrative surface they
// are entitled to.
func (u User) HasSecondFactor() bool { return u.TOTPEnabled || u.Email2FAEnabled }

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

// usernameShape mirrors app_user_username_shape in migration 000037. Both sides
// exist on purpose: the handler can explain what is wrong, and the database
// refuses a row no matter which code path wrote it.
var usernameShape = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,30}[a-z0-9]$`)

// reservedUsernames are refused because a login screen that greets "admin" or
// "support" hands a social-engineering prop to whoever claimed it first. None
// of them collide with a route — `/go/` and `/n/` are path namespaces, not user
// ones — so this is about people, not about routing.
var reservedUsernames = map[string]bool{
	"admin": true, "administrator": true, "root": true, "system": true,
	"support": true, "security": true, "foldex": true, "api": true,
	"me": true, "null": true, "undefined": true,
}

// ReservedUsername reports whether a NORMALIZED username is on the list above.
//
// Exported because NormalizeUsername deliberately collapses "wrong shape" and
// "reserved" into one error — the write path needs only "invalid" — while a
// form has to tell them apart, since they have different fixes. Without this,
// the only way to classify is to re-read the map from outside, which goes stale
// silently the day the rule stops being an exact-match lookup.
func ReservedUsername(norm string) bool { return reservedUsernames[norm] }

// ErrUsernameShape is the one refusal every bad username produces, so the
// handler does not have to enumerate them and the message stays honest about
// the actual rule.
var ErrUsernameShape = errors.New("username does not meet the required shape")

// NormalizeUsername lowercases and trims, the same shape NormalizeEmail has —
// and then REFUSES anything the login lookup could not tell apart from an
// address.
//
// The `@` rejection is the load-bearing half. Login resolves a single
// identifier against `email_normalized` OR `username_normalized`, so a username
// shaped like an address would sit in the same namespace as everybody's
// mailbox: claim `someone@example.com` as a username and every password attempt
// meant for that account arrives at yours instead. The regexp already excludes
// `@`; this is stated separately because deleting it from the character class
// would look like a widening, not a takeover.
func NormalizeUsername(username string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(username))
	if norm == "" {
		return "", nil
	}
	if strings.Contains(norm, "@") || !usernameShape.MatchString(norm) {
		return "", ErrUsernameShape
	}
	if ReservedUsername(norm) {
		return "", ErrUsernameShape
	}
	return norm, nil
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

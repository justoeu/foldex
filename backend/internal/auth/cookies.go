package auth

import (
	"net/http"
	"time"
)

// Cookie names. The fx_ prefix keeps them greppable in logs and browser
// devtools, and distinct from anything a reverse proxy might set.
const (
	CookieAccess  = "fx_at"
	CookieRefresh = "fx_rt"
	CookieCSRF    = "fx_csrf"
	// CookiePreAuth carries the between-password-and-2FA state. Unused until
	// PR3 mints challenges, but cleared here so a stale one from a partial
	// login never survives a fresh sign-in.
	CookiePreAuth = "fx_pa"
)

// CSRFHeader is the double-submit header. Its value is compared against the
// hash stored on the SESSION ROW, not against the cookie — see VerifyCSRF.
const CSRFHeader = "X-Foldex-CSRF"

// refreshPath scopes the refresh cookie to the only routes that consume it.
// A cookie the browser never sends on /api/links cannot leak through an XSS on
// a content page, and cannot be logged by a proxy that logs request headers.
const refreshPath = "/api/auth"

// CookieOptions is the per-deployment half of cookie policy.
type CookieOptions struct {
	// Secure marks the cookies HTTPS-only. It is true in every deployment that
	// terminates TLS; the plain-HTTP dev server has to turn it off, because a
	// browser silently drops a Secure cookie over http:// and the resulting
	// symptom — login "succeeds" and the next request is anonymous — is
	// spectacularly hard to read.
	Secure bool
	// Domain is normally empty (host-only cookies), which is the safer default:
	// a host-only cookie is not sent to sibling subdomains.
	Domain string
}

func (o CookieOptions) base(name, value, path string, sameSite http.SameSite, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		Domain:   o.Domain,
		MaxAge:   maxAge,
		Secure:   o.Secure,
		SameSite: sameSite,
		HttpOnly: true,
	}
}

// SetSession writes the three cookies a live session needs.
//
// SameSite differs per cookie on purpose. fx_at is Lax rather than Strict
// because a user following a link from an e-mail into foldex should land
// already signed in — and Lax is enough, since the CSRF header still gates
// every unsafe verb. fx_rt is Strict because nothing legitimate ever navigates
// cross-site directly to /api/auth/refresh.
func (o CookieOptions) SetSession(w http.ResponseWriter, tok issuedTokens) {
	// A live session and a half-finished login are mutually exclusive states,
	// so establishing one ends the other HERE rather than at each call site.
	// The 2FA path already cleared it; the OAuth and password paths did not, so
	// a challenge abandoned mid-flight left its cookie sitting next to a fresh
	// session for the rest of its TTL. Not redeemable without the code, but the
	// guarantee should come from the one function that defines "signed in on
	// the wire", not from four callers each remembering.
	o.ClearPreAuth(w)

	http.SetCookie(w, o.base(CookieAccess, tok.Access, "/", http.SameSiteLaxMode,
		int(time.Until(tok.AccessExpiry).Seconds())))
	http.SetCookie(w, o.base(CookieRefresh, tok.Refresh, refreshPath, http.SameSiteStrictMode,
		int(time.Until(tok.RefreshExpiry).Seconds())))

	// The CSRF cookie is the ONE that is deliberately readable by JavaScript:
	// the SPA has to copy its value into the X-Foldex-CSRF header. That is not
	// a weakness — its whole security value is that a cross-origin attacker
	// cannot READ it (same-origin policy), not that a script on our own page
	// cannot.
	csrf := o.base(CookieCSRF, tok.CSRF, "/", http.SameSiteLaxMode,
		int(time.Until(tok.RefreshExpiry).Seconds()))
	csrf.HttpOnly = false
	http.SetCookie(w, csrf)
}

// SetPreAuth writes the between-password-and-second-factor cookie.
//
// It is scoped to /api/auth like the refresh cookie, and for a stronger reason:
// this credential proves only that a password was accepted, so it must not be
// attached to a single data route. Strict SameSite because nothing legitimate
// ever navigates cross-site into a half-finished login.
func (o CookieOptions) SetPreAuth(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, o.base(CookiePreAuth, token, refreshPath, http.SameSiteStrictMode, int(ttl.Seconds())))
}

// ClearPreAuth expires the pre-auth cookie without touching the session.
//
// Needed on its own because the two lifecycles diverge: finishing a second
// factor clears the pre-auth cookie and SETS the session cookies, so calling
// ClearSession there would wipe the credentials just issued.
func (o CookieOptions) ClearPreAuth(w http.ResponseWriter) {
	ck := o.base(CookiePreAuth, "", refreshPath, http.SameSiteStrictMode, -1)
	ck.Expires = time.Unix(0, 0)
	http.SetCookie(w, ck)
}

// ClearSession expires every auth cookie.
//
// Each one is cleared with the SAME path it was set with. A browser keys
// cookies by (name, domain, path), so clearing fx_rt at "/" would leave the
// real cookie at /api/auth untouched — a "logout" that leaves a working
// refresh token behind.
func (o CookieOptions) ClearSession(w http.ResponseWriter) {
	for _, c := range []struct {
		name string
		path string
	}{
		{CookieAccess, "/"},
		{CookieCSRF, "/"},
		{CookieRefresh, refreshPath},
		{CookiePreAuth, refreshPath},
	} {
		ck := o.base(c.name, "", c.path, http.SameSiteLaxMode, -1)
		ck.Expires = time.Unix(0, 0)
		if c.name == CookieCSRF {
			ck.HttpOnly = false
		}
		http.SetCookie(w, ck)
	}
}

// cookieValue returns a cookie's value, or "" when absent.
func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

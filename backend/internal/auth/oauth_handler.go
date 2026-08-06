package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"foldex/internal/mailer"
	"foldex/internal/oauthgoogle"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// GoogleProvider is the slice of the OAuth client this handler uses.
//
// An interface rather than *oauthgoogle.Provider so the integration tests can
// drive the whole callback — state matching, the conversion divert, the 2FA
// hop — against a double, without the endpoint URLs becoming configuration.
// Making them configurable would be the alternative, and it is a worse one: an
// operator-settable token endpoint is a credential-exfiltration channel wearing
// a config knob.
type GoogleProvider interface {
	Enabled() bool
	AuthCodeURL(state, challenge string) (string, error)
	Exchange(ctx context.Context, code, verifier string) (string, error)
	UserInfo(ctx context.Context, accessToken string) (oauthgoogle.UserInfo, error)
}

// oauthStateTTL bounds the redirect round-trip. Long enough to read a consent
// screen and pick an account, short enough that an abandoned redirect is not a
// standing invitation to complete someone else's flow.
const oauthStateTTL = 10 * time.Minute

// CookieOAuth carries the redirect state. It is the browser half of the pair
// whose server half lives in oauth_state; a callback must present BOTH, which
// is what stops an attacker from grafting their own Google account onto someone
// else's session by feeding them a callback URL.
const CookieOAuth = "fx_oauth"

// oauthCookiePath scopes fx_oauth to the callback that consumes it.
const oauthCookiePath = "/api/auth/oauth"

// SetOAuthState writes the redirect-state cookie.
//
// SameSite=Lax, not Strict. The callback arrives as a top-level navigation FROM
// accounts.google.com, and a Strict cookie is not sent on a cross-site
// navigation — the flow would fail on every attempt with a state mismatch. Lax
// is exactly the case this setting exists for: sent on top-level GET
// navigations, withheld from cross-site subrequests.
func (o CookieOptions) SetOAuthState(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, o.base(CookieOAuth, token, oauthCookiePath, http.SameSiteLaxMode, int(ttl.Seconds())))
}

// ClearOAuthState expires the redirect-state cookie.
func (o CookieOptions) ClearOAuthState(w http.ResponseWriter) {
	ck := o.base(CookieOAuth, "", oauthCookiePath, http.SameSiteLaxMode, -1)
	ck.Expires = time.Unix(0, 0)
	http.SetCookie(w, ck)
}

// ─────────────────────────────────────────────────────────────────────
// Start
// ─────────────────────────────────────────────────────────────────────

// OAuthStart mints PKCE + state and redirects the browser to Google.
//
// A GET that writes a row, which is safe here for a specific reason: the row is
// useless without the cookie set in the same response, and the callback demands
// both. An attacker who can make a victim's browser hit this endpoint achieves
// nothing except an extra oauth_state row — which is why it is rate-limited by
// IP rather than protected by CSRF, a header a top-level navigation cannot send.
func (h *Handler) OAuthStart(w http.ResponseWriter, r *http.Request) {
	if !h.oauthEnabled() {
		httperr.Write(w, errOAuthDisabled())
		return
	}
	key := "oauth:" + clientIP(r)
	if until, ok := h.oauthIP.Begin(key); !ok {
		writeRateLimited(w, until)
		return
	}
	// CHARGED, not released on success. There is no success/failure distinction
	// here: every start writes an oauth_state row, so the thing worth bounding
	// is the request itself. CommitSuccess would DELETE the bucket entry, which
	// would reset the counter on every call and make the cap decorative — the
	// same reasoning ForgotPassword's "every request is charged" comment gives.
	defer h.oauthIP.CommitFail(key)

	purpose := r.URL.Query().Get("purpose")
	if purpose == "" {
		purpose = OAuthPurposeLogin
	}

	var owner *authctx.UserID
	var inviteID *int64

	switch purpose {
	case OAuthPurposeLogin:
	case OAuthPurposeLink:
		// The account to link is the one the SESSION proves, resolved NOW and
		// stored on the state row. Reading it at callback time instead would
		// bind whatever session happens to exist when Google redirects back —
		// and the redirect is attacker-timeable.
		p, ok := authctx.FromContext(r.Context())
		if !ok {
			httperr.Write(w, httperr.New(http.StatusUnauthorized, "unauthorized",
				"sign in before linking an account"))
			return
		}
		uid := p.UserID
		owner = &uid
	case OAuthPurposeAcceptInvite:
		inv, err := h.repo.LookupInvite(r.Context(), r.URL.Query().Get("invite"))
		if err != nil {
			httperr.Write(w, httperr.New(http.StatusNotFound, "invite_invalid",
				"this invitation is no longer valid"))
			return
		}
		id := inv.ID
		inviteID = &id
	default:
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_purpose", "unknown purpose"))
		return
	}

	pkce, err := oauthgoogle.NewPKCE()
	if err != nil {
		h.logger.Error("oauth pkce", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	state, err := h.repo.CreateOAuthState(r.Context(), ProviderGoogle, purpose, pkce.Verifier,
		owner, inviteID, oauthStateTTL)
	if err != nil {
		h.logger.Error("oauth state", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	target, err := h.google.AuthCodeURL(state, pkce.Challenge)
	if err != nil {
		h.logger.Error("oauth auth url", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.cookies.SetOAuthState(w, state, oauthStateTTL)
	http.Redirect(w, r, target, http.StatusFound)
}

// ─────────────────────────────────────────────────────────────────────
// Callback
// ─────────────────────────────────────────────────────────────────────

// OAuthCallback finishes the round-trip.
//
// Every exit is a redirect back to the SPA, never a JSON body: this handler
// answers a top-level navigation from Google, so a payload here would render as
// raw text in the address bar. Outcomes travel as `?oauth=` / `?oauth_error=`
// and the SPA reads them at module scope; the actual STATE (a session, or a
// pending challenge) travels in cookies, and the SPA discovers it by calling
// /me on boot.
func (h *Handler) OAuthCallback(w http.ResponseWriter, r *http.Request) {
	h.cookies.ClearOAuthState(w)

	if !h.oauthEnabled() {
		h.oauthRedirectError(w, r, "oauth_disabled")
		return
	}
	q := r.URL.Query()

	// Google reports a refusal in the query string, not as an HTTP error. The
	// commonest value by far is access_denied — the user pressed Cancel — and it
	// deserves its own message rather than "something went wrong".
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			h.oauthRedirectError(w, r, "cancelled")
			return
		}
		h.oauthRedirectError(w, r, "provider_error")
		return
	}

	// The state must match on BOTH sides: the row the server minted and the
	// cookie the browser carries. Checking only the row would accept a state an
	// attacker obtained in their own browser and then fed to a victim.
	rawState := q.Get("state")
	if rawState == "" || rawState != cookieValue(r, CookieOAuth) {
		h.oauthRedirectError(w, r, "state_invalid")
		return
	}
	st, err := h.repo.ConsumeOAuthState(r.Context(), rawState)
	if err != nil {
		h.oauthRedirectError(w, r, "state_invalid")
		return
	}

	code := q.Get("code")
	if code == "" {
		h.oauthRedirectError(w, r, "state_invalid")
		return
	}
	token, err := h.google.Exchange(r.Context(), code, st.Verifier)
	if err != nil {
		h.logger.Warn("oauth exchange failed", "err", err)
		h.oauthRedirectError(w, r, "provider_error")
		return
	}
	info, err := h.google.UserInfo(r.Context(), token)
	if err != nil {
		h.logger.Warn("oauth userinfo failed", "err", err)
		h.oauthRedirectError(w, r, "provider_error")
		return
	}

	switch st.Purpose {
	case OAuthPurposeLink:
		h.oauthFinishLink(w, r, st, info)
	case OAuthPurposeAcceptInvite:
		h.oauthFinishInvite(w, r, st, info)
	default:
		h.oauthFinishLogin(w, r, info)
	}
}

// oauthFinishLogin resolves the Google subject onto an account, or offers the
// conversion.
func (h *Handler) oauthFinishLogin(w http.ResponseWriter, r *http.Request, info oauthgoogle.UserInfo) {
	user, err := h.repo.UserByIdentity(r.Context(), ProviderGoogle, info.Subject)
	switch {
	case err == nil:
		if user.Status != StatusActive {
			h.oauthRedirectError(w, r, "not_linked")
			return
		}
		h.repo.TouchIdentity(r.Context(), ProviderGoogle, info.Subject)
		h.oauthComplete(w, r, user)
		return
	case !errors.Is(err, ErrNoUser):
		h.logger.Error("oauth identity lookup", "err", err)
		h.oauthRedirectError(w, r, "server_error")
		return
	}

	// Unknown subject. The e-mail may still match an account that signs in with
	// a password — that is the CONVERSION case, and it is the only thing a
	// matching address is allowed to unlock. It never produces a session here.
	//
	// The verified check comes BEFORE the lookup, deliberately. An unverified
	// Google address is a string somebody typed, so it must not reach the
	// conversion prompt either way — and answering `email_unverified` only when
	// the address happens to match an account would turn this endpoint into an
	// existence oracle: create a Google account claiming victim@example.com,
	// leave it unverified, and the difference between the two answers says
	// whether that address has a foldex account. Everything else on this surface
	// (disabled == unknown, login byte-identical) is built to avoid exactly
	// that, and the cost here is a vaguer message for a rare honest case.
	if !info.EmailVerified {
		h.oauthRedirectError(w, r, "not_linked")
		return
	}
	candidate, err := h.repo.UserByEmail(r.Context(), info.Email)
	if err != nil {
		// Unknown address, and no auto-provisioning: an instance is invite-only,
		// so anyone with a Google account being able to create one would be a
		// silent bypass of that policy.
		if !errors.Is(err, ErrNoUser) {
			h.logger.Error("oauth email lookup", "err", err)
		}
		h.oauthRedirectError(w, r, "not_linked")
		return
	}
	if candidate.Status != StatusActive {
		// The SAME answer an unknown address gets. A distinct one would confirm
		// that the account exists and is merely disabled.
		h.oauthRedirectError(w, r, "not_linked")
		return
	}
	if !candidate.HasPassword {
		// Nothing to convert: the account has no password to prove. This is
		// unreachable through the UI today (an account without a password has an
		// identity, and the identity lookup above would have matched), but it
		// would otherwise open a challenge that can never be satisfied.
		h.oauthRedirectError(w, r, "not_linked")
		return
	}

	// The Google subject is pinned onto the challenge row, not sent to the
	// browser: the convert POST must attach the identity Google actually
	// vouched for, never one the client names.
	ok := h.beginChallengeFor(w, r, NewChallenge{
		UserID:    candidate.ID,
		Purpose:   PurposeConvertGoogle,
		TTL:       challengeTTL,
		IP:        clientIP(r),
		UserAgent: r.UserAgent(),
		Identity: &linkedIdentity{
			provider: ProviderGoogle, subject: info.Subject, email: info.Email,
		},
	})
	if !ok {
		return
	}
	h.oauthRedirect(w, r, "convert")
}

// oauthFinishLink attaches the provider to the account the START-time session
// proved.
func (h *Handler) oauthFinishLink(w http.ResponseWriter, r *http.Request, st OAuthState, info oauthgoogle.UserInfo) {
	if st.UserID == nil {
		h.oauthRedirectError(w, r, "state_invalid")
		return
	}
	// The addresses need NOT match. Linking a personal Gmail to a work account
	// is legitimate precisely because the session already proved possession —
	// which is also why linking without a session can never be allowed.
	err := h.repo.LinkIdentity(r.Context(), *st.UserID, ProviderGoogle, info.Subject, info.Email)
	switch {
	case errors.Is(err, ErrIdentityTaken):
		h.oauthRedirectError(w, r, "already_linked")
	case errors.Is(err, ErrIdentityExists):
		h.oauthRedirectError(w, r, "already_linked")
	case err != nil:
		h.logger.Error("oauth link", "err", err)
		h.oauthRedirectError(w, r, "server_error")
	default:
		h.oauthRedirect(w, r, "linked")
	}
}

// oauthFinishInvite claims an invitation with a Google account.
func (h *Handler) oauthFinishInvite(w http.ResponseWriter, r *http.Request, st OAuthState, info oauthgoogle.UserInfo) {
	if st.InviteID == nil {
		h.oauthRedirectError(w, r, "state_invalid")
		return
	}
	if !info.EmailVerified {
		h.oauthRedirectError(w, r, "email_unverified")
		return
	}
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = info.Email
	}
	user, err := h.repo.AcceptInviteWithIdentityByID(r.Context(), *st.InviteID, name,
		ProviderGoogle, info.Subject, info.Email)
	switch {
	case errors.Is(err, ErrInviteEmailMismatch):
		h.oauthRedirectError(w, r, "invite_email_mismatch")
	case errors.Is(err, ErrInviteInvalid):
		h.oauthRedirectError(w, r, "invite_invalid")
	case errors.Is(err, ErrIdentityTaken):
		h.oauthRedirectError(w, r, "already_linked")
	case errors.Is(err, ErrEmailTaken):
		h.oauthRedirectError(w, r, "email_taken")
	case err != nil:
		h.logger.Error("oauth accept invite", "err", err)
		h.oauthRedirectError(w, r, "server_error")
	default:
		h.oauthComplete(w, r, user)
	}
}

// oauthComplete issues a session — or diverts into the second factor, exactly
// as a password login does.
//
// This is the anti-bypass guarantee. With TOTP mandatory for admins, an OAuth
// path that minted a session directly would be a hole straight through the
// policy: "sign in with Google" would be strictly weaker than the password it
// replaces.
func (h *Handler) oauthComplete(w http.ResponseWriter, r *http.Request, user User) {
	if purpose := h.secondFactorPurpose(user); purpose != "" {
		if !h.beginChallenge(w, r, user, purpose, false) {
			return
		}
		h.oauthRedirect(w, r, "two_factor")
		return
	}
	tok, _, err := h.repo.IssueSession(r.Context(), user.ID, h.ttl, clientIP(r), r.UserAgent())
	if err != nil {
		h.logger.Error("oauth issue session", "err", err)
		h.oauthRedirectError(w, r, "server_error")
		return
	}
	h.cookies.SetSession(w, tok)
	h.oauthRedirect(w, r, "signed_in")
}

// ─────────────────────────────────────────────────────────────────────
// Convert
// ─────────────────────────────────────────────────────────────────────

type oauthConvertInput struct {
	Password string `json:"password"`
}

// OAuthConvert exchanges the account's CURRENT PASSWORD for a Google identity,
// retiring the password in the same transaction.
//
// The password is the proof, deliberately — not the mailbox. That makes this
// stricter than the reset flow, which does hand the account to whoever controls
// the address. The choice is that conversion is a migration the owner performs,
// not a recovery path: someone who has taken over a mailbox cannot use it to
// swap the account's credential for one they hold.
//
// Accepted consequence: "I forgot my password, let me just use Google" does not
// work. Reset first, convert after.
func (h *Handler) OAuthConvert(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer floorDuration(start, loginFloor)

	if !h.oauthEnabled() {
		httperr.Write(w, errOAuthDisabled())
		return
	}
	ch, ok := h.requireChallenge(w, r, PurposeConvertGoogle)
	if !ok {
		return
	}
	in, err := httperr.DecodeJSON[oauthConvertInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	// Charged BEFORE the password is checked. A cancelled request must not be a
	// free guess, and the budget lives on the challenge row so a restart cannot
	// hand the attacker a fresh one.
	if _, err := h.repo.BumpChallengeAttempt(r.Context(), ch.ID); err != nil {
		h.writeChallengeError(w, err)
		return
	}

	if ch.OAuthSubject == "" {
		// A convert challenge without a subject cannot exist through any code
		// path; treating it as an internal error rather than silently linking
		// nothing is what keeps that true.
		h.logger.Error("convert challenge has no subject", "challenge", ch.ID)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	switch err := h.repo.VerifyUserPassword(r.Context(), ch.UserID, in.Password); {
	case errors.Is(err, ErrBadCredentials), errors.Is(err, ErrPasswordMissing):
		httperr.Write(w, errInvalidCredentials())
		return
	case err != nil:
		h.logger.Error("convert verify", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	user, err := h.repo.GetUser(r.Context(), ch.UserID)
	if err != nil {
		h.logger.Error("convert load user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if err := h.repo.ConvertToProvider(r.Context(), ch.UserID, ProviderGoogle,
		ch.OAuthSubject, ch.OAuthEmail, ch.ID); err != nil {
		if errors.Is(err, ErrIdentityTaken) || errors.Is(err, ErrIdentityExists) {
			// The subject was linked elsewhere between the callback and this
			// request. Rare, but it is the window a racing attacker would aim at.
			httperr.Write(w, httperr.New(http.StatusConflict, "oauth_already_linked",
				"that Google account is already linked to another user"))
			return
		}
		h.logger.Error("convert", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// Cleared BEFORE completeLogin, not after: when the account owes a second
	// factor, completeLogin mints a fresh pre-auth cookie, and the later
	// Set-Cookie is the one the browser keeps. Clearing afterwards would delete
	// the credential the code screen is about to need.
	h.cookies.ClearPreAuth(w)
	h.sendAsync(mailer.AccountConvertedMessage(user.Email, ch.OAuthEmail), "account converted")

	// The password is gone, but the second factor is not: an account with an
	// authenticator still owes a code. completeLogin is what decides that, and
	// routing through it is why conversion cannot be used to shed 2FA.
	user.HasPassword = false
	h.completeLogin(w, r, user, false)
}

// ─────────────────────────────────────────────────────────────────────
// Unlink
// ─────────────────────────────────────────────────────────────────────

type oauthUnlinkInput struct {
	Password string `json:"password"`
}

// OAuthUnlink detaches Google, requiring the account's password.
//
// Requiring the password is what makes this the second half of the lockout
// escape hatch: an account that converted to Google-only must first set a
// password (Settings → "set a password"), and only then can it unlink. Doing it
// in the other order would leave no credential at all — which the database's
// constraint trigger would refuse anyway, but as a 500 rather than as something
// the user can act on.
func (h *Handler) OAuthUnlink(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	in, err := httperr.DecodeJSON[oauthUnlinkInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	switch err := h.repo.VerifyUserPassword(r.Context(), p.UserID, in.Password); {
	case errors.Is(err, ErrPasswordMissing):
		httperr.Write(w, httperr.New(http.StatusConflict, "password_required",
			"set a password before unlinking your Google account"))
		return
	case errors.Is(err, ErrBadCredentials):
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
			"current password is incorrect"))
		return
	case err != nil:
		h.logger.Error("unlink verify", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	switch err := h.repo.UnlinkIdentity(r.Context(), p.UserID, ProviderGoogle); {
	case errors.Is(err, ErrIdentityMissing):
		httperr.Write(w, httperr.New(http.StatusNotFound, "not_linked",
			"no Google account is linked"))
	case errors.Is(err, ErrLastCredential):
		httperr.Write(w, httperr.New(http.StatusConflict, "password_required",
			"set a password before unlinking your Google account"))
	case err != nil:
		h.logger.Error("unlink", "err", err)
		httperr.Write(w, httperr.ErrInternal)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ListIdentities reports which providers the caller has linked, so the account
// screen can offer "connect" or "disconnect" rather than guessing.
func (h *Handler) ListIdentities(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	list, err := h.repo.ListIdentities(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("list identities", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"identities": list})
}

// ─────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────

func (h *Handler) oauthEnabled() bool {
	return h.google != nil && h.google.Enabled()
}

func errOAuthDisabled() error {
	return httperr.New(http.StatusNotImplemented, "oauth_disabled",
		"Google sign-in is not configured on this instance")
}

// oauthRedirect sends the browser back to the SPA with an outcome marker.
//
// The target is built from the CONFIGURED base URL, never from the request:
// Host and X-Forwarded-Host are attacker-supplied, and a redirect built from
// them is an open redirect on an endpoint that has just set session cookies.
func (h *Handler) oauthRedirect(w http.ResponseWriter, r *http.Request, outcome string) {
	h.redirectToSPA(w, r, "oauth", outcome)
}

func (h *Handler) oauthRedirectError(w http.ResponseWriter, r *http.Request, code string) {
	h.redirectToSPA(w, r, "oauth_error", code)
}

func (h *Handler) redirectToSPA(w http.ResponseWriter, r *http.Request, key, value string) {
	q := url.Values{key: {value}}
	// An empty base yields a ROOT-relative path, never "//?…": a leading double
	// slash is protocol-relative, and this endpoint has just set session
	// cookies — the one place an accidental open redirect costs the most.
	// AUTH_PUBLIC_URL always has a value in practice; this is what keeps that
	// from being load-bearing.
	target := "/?" + q.Encode()
	if h.baseURL != "" {
		target = h.baseURL + target
	}
	http.Redirect(w, r, target, http.StatusFound)
}

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
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
const (
	oauthStateTTL     = 10 * time.Minute
	oauthLinkProofTTL = 5 * time.Minute
)

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
	// #nosec G124 -- expiring an empty-value cookie; attributes mirror the set path.
	ck := o.base(CookieOAuth, "", oauthCookiePath, http.SameSiteLaxMode, -1)
	ck.Expires = time.Unix(0, 0)
	http.SetCookie(w, ck)
}

// ─────────────────────────────────────────────────────────────────────
// Start
// ─────────────────────────────────────────────────────────────────────

// OAuthStart mints PKCE + state for login or invite acceptance and redirects
// the browser to Google. Linking uses OAuthLinkStart instead.
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

	switch purpose {
	case OAuthPurposeLogin:
	case OAuthPurposeLink:
		httperr.Write(w, httperr.New(http.StatusMethodNotAllowed, "oauth_link_step_up_required",
			"linking must start with a credential proof"))
		return
	case OAuthPurposeAcceptInvite:
		httperr.Write(w, httperr.New(http.StatusMethodNotAllowed, "oauth_invite_post_required",
			"invitation OAuth must start with a POST body"))
		return
	default:
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_purpose", "unknown purpose"))
		return
	}

	state, target, err := h.oauthStartTarget(r.Context(), purpose, nil, nil, oauthStateTTL)
	if err != nil {
		h.logger.Error("oauth start", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.cookies.SetOAuthState(w, state, oauthStateTTL)
	http.Redirect(w, r, target, http.StatusFound) // #nosec G710 -- target is Google's constant auth endpoint plus our own params, never request input. nosemgrep: go.lang.security.injection.open-redirect.open-redirect
}

type oauthInviteStartInput struct {
	Invite string `json:"invite"`
}

// OAuthInviteStart resolves the invitation from a POST body and returns the
// provider URL as JSON. Optional authentication is intentional: anonymous
// invitees can start, while an existing session still receives CSRF protection
// from Middleware.Optional before this handler runs.
func (h *Handler) OAuthInviteStart(w http.ResponseWriter, r *http.Request) {
	if !h.oauthEnabled() {
		httperr.Write(w, errOAuthDisabled())
		return
	}
	in, err := httperr.DecodeJSON[oauthInviteStartInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	key := "oauth:" + clientIP(r)
	if until, ok := h.oauthIP.Begin(key); !ok {
		writeRateLimited(w, until)
		return
	}
	defer h.oauthIP.CommitFail(key)

	inv, err := h.repo.LookupInvite(r.Context(), in.Invite)
	if err != nil {
		httperr.Write(w, httperr.New(http.StatusNotFound, "invite_invalid",
			"this invitation is no longer valid"))
		return
	}
	state, target, err := h.oauthStartTarget(r.Context(), OAuthPurposeAcceptInvite, &inv.ID, nil, oauthStateTTL)
	if err != nil {
		h.logger.Error("oauth invite start", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.cookies.SetOAuthState(w, state, oauthStateTTL)
	httperr.JSON(w, http.StatusOK, map[string]string{"redirect_url": target})
}

type oauthLinkStartInput struct {
	CurrentPassword string `json:"current_password"`
	Code            string `json:"code"`
}

type oauthLinkProof struct {
	UserID       authctx.UserID
	SessionID    int64
	TokenVersion int
}

// OAuthLinkStart requires fresh credentials before minting a state that can add
// a new sign-in method. The proofs stay in the POST body and never enter a URL.
func (h *Handler) OAuthLinkStart(w http.ResponseWriter, r *http.Request) {
	defer floorDuration(time.Now(), loginFloor)
	if !h.oauthEnabled() {
		httperr.Write(w, errOAuthDisabled())
		return
	}
	in, err := httperr.DecodeJSON[oauthLinkStartInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	p, _ := authctx.FromContext(r.Context())
	passwordKey := "stepup-password:" + strconv.FormatInt(int64(p.UserID), 10)
	if until, ok := h.stepUpPasswordUser.Begin(passwordKey); !ok {
		writeRateLimited(w, until)
		return
	}
	tokenVersion, err := h.repo.VerifyUserPasswordEpoch(r.Context(), p.UserID, in.CurrentPassword)
	switch {
	case errors.Is(err, ErrBadCredentials), errors.Is(err, ErrPasswordMissing):
		h.stepUpPasswordUser.CommitFail(passwordKey)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
			"current password is incorrect"))
		return
	case err != nil:
		h.stepUpPasswordUser.Release(passwordKey)
		h.logger.Error("oauth link password verification", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	default:
		h.stepUpPasswordUser.CommitSuccess(passwordKey)
	}

	user, err := h.repo.GetUser(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("oauth link load user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if user.TOTPEnabled && !h.checkOAuthLinkSecondFactor(w, r, user, in.Code) {
		return
	}

	oauthKey := "oauth:" + clientIP(r)
	if until, ok := h.oauthIP.Begin(oauthKey); !ok {
		writeRateLimited(w, until)
		return
	}
	defer h.oauthIP.CommitFail(oauthKey)

	proof := &oauthLinkProof{UserID: p.UserID, SessionID: p.SessionID, TokenVersion: tokenVersion}
	state, target, err := h.oauthStartTarget(r.Context(), OAuthPurposeLink, nil, proof, oauthLinkProofTTL)
	if errors.Is(err, ErrOAuthLinkInvalid) {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
			"credential proof is no longer valid"))
		return
	}
	if err != nil {
		h.logger.Error("oauth link start", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.cookies.SetOAuthState(w, state, oauthLinkProofTTL)
	httperr.JSON(w, http.StatusOK, map[string]string{"redirect_url": target})
}

func (h *Handler) checkOAuthLinkSecondFactor(w http.ResponseWriter, r *http.Request, user User, code string) bool {
	key := "stepup:" + strconv.FormatInt(int64(user.ID), 10)
	until, ok := h.stepUpUser.Begin(key)
	if !ok {
		writeRateLimited(w, until)
		return false
	}

	method := ""
	if digits, numeric := numericOTP(code); numeric && h.cipher != nil {
		proof := h.verifyTOTPProof(r.Context(), user.ID, digits)
		if proof != nil && h.repo.ConsumeTOTPProof(r.Context(), user.ID, *proof) == nil {
			method = methodTOTP
		}
	} else if normalized := normalizeRecoveryCode(code); len(normalized) == recoveryCodeChars && h.codeMAC != nil {
		digest := h.codeMAC.RecoveryCodeDigest(user.ID, normalized)
		if h.repo.ConsumeRecoveryCode(r.Context(), user.ID, digest) == nil {
			method = methodRecovery
		}
	}
	if method == "" {
		h.stepUpUser.CommitFail(key)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_code", "that code is not valid"))
		return false
	}
	h.stepUpUser.CommitSuccess(key)
	if method == methodRecovery {
		h.notifyRecoveryCodeUsed(r.Context(), user, localeFor(user.Locale, r))
	}
	return true
}

func (h *Handler) oauthStartTarget(ctx context.Context, purpose string, inviteID *int64,
	proof *oauthLinkProof, ttl time.Duration) (state, target string, err error) {
	pkce, err := oauthgoogle.NewPKCE()
	if err != nil {
		return "", "", err
	}
	if proof == nil {
		state, err = h.repo.CreateOAuthState(ctx, ProviderGoogle, purpose, pkce.Verifier, nil, inviteID, ttl)
	} else {
		state, err = h.repo.CreateOAuthLinkState(ctx, ProviderGoogle, pkce.Verifier,
			proof.UserID, proof.SessionID, proof.TokenVersion, ttl)
	}
	if err != nil {
		return "", "", err
	}
	target, err = h.google.AuthCodeURL(state, pkce.Challenge)
	if err != nil {
		return "", "", err
	}
	// The state token travels in this URL, so absolute HTTPS is mandatory even
	// if a future provider implementation stops using a package constant.
	if !strings.HasPrefix(target, "https://") {
		return "", "", errors.New("oauth auth URL is not absolute HTTPS")
	}
	return state, target, nil
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
	if st.Purpose == OAuthPurposeLink {
		err := h.repo.ValidateOAuthLinkProof(r.Context(), st,
			cookieValue(r, CookieAccess), oauthLinkProofTTL)
		switch {
		case errors.Is(err, ErrOAuthLinkInvalid):
			h.oauthRedirectError(w, r, "state_invalid")
			return
		case err != nil:
			h.logger.Error("oauth link proof", "err", err)
			h.oauthRedirectError(w, r, "server_error")
			return
		}
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
// oauthDomainAllowed reports whether the policy lets this address join.
//
// A nil policy allows everything, matching the compiled-in behaviour of an
// instance that never opened the settings screen.
func (h *Handler) oauthDomainAllowed(ctx context.Context, email string) bool {
	if h.policy == nil {
		return true
	}
	return h.policy.GoogleAllows(ctx, email)
}

// oauthAutoProvision creates an account for a Google user who has none, when
// the owner has explicitly enabled it.
//
// Every refusal is the SAME `not_linked` an unknown address has always
// produced. A distinct "auto-provisioning is disabled" would tell an anonymous
// caller which instances are open, and a distinct "your domain is not allowed"
// would let them enumerate the allowlist one guess at a time.
func (h *Handler) oauthAutoProvision(w http.ResponseWriter, r *http.Request, info oauthgoogle.UserInfo) {
	if h.policy == nil {
		h.oauthRedirectError(w, r, "not_linked")
		return
	}
	enabled, role := h.policy.GoogleProvisioning(r.Context())
	// Re-checked here rather than trusted from the policy: this is the one
	// branch that mints an account, and the role it writes must never be
	// administrative even if a row was edited past policy.Validate.
	if !enabled || (role != authctx.RoleEditor && role != authctx.RoleViewer) {
		h.oauthRedirectError(w, r, "not_linked")
		return
	}

	user, err := h.repo.ProvisionOAuthUser(r.Context(), info.Email, info.Name,
		ProviderGoogle, info.Subject, role)
	if err != nil {
		// A concurrent request for the same address loses the unique index and
		// lands here. Answering not_linked is correct rather than merely safe:
		// the winner's account exists now, and a retry resolves by subject.
		h.logger.Error("oauth auto-provision", "err", err)
		h.oauthRedirectError(w, r, "not_linked")
		return
	}
	// Straight into oauthComplete, exactly like a linked login — which is what
	// keeps the second-factor policy applying to a freshly provisioned account
	// instead of it being the one door that skips the check.
	h.oauthComplete(w, r, user)
}

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
	// The domain allowlist gates the two paths that CREATE access — conversion
	// below and auto-provisioning — and deliberately not the linked login above.
	//
	// Applying it to an existing identity would let an owner lock themselves out
	// of their own instance by saving a list that excludes their own domain, and
	// a Google-only owner would have no second way in. An already-linked
	// identity is access the owner granted on purpose; this setting decides who
	// may JOIN, which is also how the administration screen presents it.
	if !h.oauthDomainAllowed(r.Context(), info.Email) {
		h.oauthRedirectError(w, r, "not_linked")
		return
	}

	candidate, err := h.repo.UserByEmail(r.Context(), info.Email)
	if err != nil {
		if !errors.Is(err, ErrNoUser) {
			h.logger.Error("oauth email lookup", "err", err)
			h.oauthRedirectError(w, r, "not_linked")
			return
		}
		// Unknown address. Historically the end of the road — an instance is
		// invite-only, so anyone with a Google account being able to create one
		// would be a silent bypass of that policy. ADR-35 lets an owner revoke
		// that rule explicitly, for a named set of domains, with a default role
		// that can never be administrative.
		h.oauthAutoProvision(w, r, info)
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
	ok := h.oauthBeginChallenge(w, r, NewChallenge{
		UserID:       candidate.ID,
		Purpose:      PurposeConvertGoogle,
		TokenVersion: candidate.TokenVersion,
		TTL:          challengeTTL,
		IP:           clientIP(r),
		UserAgent:    r.UserAgent(),
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
	err := h.repo.LinkIdentity(r.Context(), st, cookieValue(r, CookieAccess),
		ProviderGoogle, info.Subject, info.Email, oauthLinkProofTTL)
	switch {
	case errors.Is(err, ErrOAuthLinkInvalid):
		h.oauthRedirectError(w, r, "state_invalid")
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
		if !h.oauthBeginChallenge(w, r, NewChallenge{
			UserID: user.ID, Purpose: purpose, TokenVersion: user.TokenVersion,
			TTL: challengeTTL, IP: clientIP(r), UserAgent: r.UserAgent(),
		}) {
			return
		}
		h.oauthRedirect(w, r, "two_factor")
		return
	}
	tok, _, err := h.repo.IssueSession(r.Context(), user.ID, user.TokenVersion,
		h.ttl, clientIP(r), r.UserAgent())
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

	user, googleEmail, err := h.repo.ConvertToProvider(r.Context(), ch.ID, in.Password)
	if err != nil {
		if errors.Is(err, ErrChallengeInvalid) {
			h.writeChallengeError(w, err)
			return
		}
		if errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrPasswordMissing) {
			httperr.Write(w, errInvalidCredentials())
			return
		}
		if errors.Is(err, ErrIdentityTaken) {
			// The subject was linked elsewhere between the callback and this
			// request. Rare, but it is the window a racing attacker would aim at.
			httperr.Write(w, httperr.New(http.StatusConflict, "oauth_already_linked",
				"that Google account is already linked to another user"))
			return
		}
		if errors.Is(err, ErrIdentityExists) {
			// THIS account already has Google linked — two convert requests
			// raced and the other one won. Saying "another user" here would be
			// simply false, and the honest answer is that it is already done.
			httperr.Write(w, httperr.New(http.StatusConflict, "already_converted",
				"this account is already linked to Google"))
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
	h.enqueueMail(r.Context(), mailer.AccountConvertedMessage(user.Email, googleEmail),
		localeFor(user.Locale, r), "account converted")

	// The password is gone, but the second factor is not: an account with an
	// authenticator still owes a code. completeLogin is what decides that, and
	// routing through it is why conversion cannot be used to shed 2FA.
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
	user, err := h.repo.GetUser(r.Context(), p.UserID)
	if err != nil {
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	switch err := h.repo.UnlinkIdentity(r.Context(), p.UserID, p.SessionID, user.TokenVersion,
		ProviderGoogle, in.Password); {
	case errors.Is(err, ErrPasswordMissing):
		httperr.Write(w, httperr.New(http.StatusConflict, "password_required",
			"set a password before unlinking your Google account"))
	case errors.Is(err, ErrBadCredentials):
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
			"current password is incorrect"))
	case errors.Is(err, ErrIdentityMissing):
		httperr.Write(w, httperr.New(http.StatusNotFound, "not_linked",
			"no Google account is linked"))
	case errors.Is(err, ErrLastCredential):
		httperr.Write(w, httperr.New(http.StatusConflict, "password_required",
			"set a password before unlinking your Google account"))
	case errors.Is(err, ErrSessionInvalid):
		h.writeSessionInvalid(w)
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

func (h *Handler) oauthBeginChallenge(w http.ResponseWriter, r *http.Request, in NewChallenge) bool {
	raw, _, err := h.repo.CreateChallenge(r.Context(), in)
	if err != nil {
		h.logger.Error("create oauth challenge", "err", err)
		h.oauthRedirectError(w, r, "server_error")
		return false
	}
	h.cookies.SetPreAuth(w, raw, challengeTTL)
	return true
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

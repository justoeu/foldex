package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"foldex/internal/mailer"
	"foldex/internal/pkg/attemptlimit"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/pwhash"
	"foldex/internal/pkg/secrets"
)

// Password policy. The minimum matches the master recovery password (ADR-29) so
// the product has ONE number to explain. The maximum is bcrypt's hard limit:
// the algorithm silently truncates at 72 bytes, so accepting a 200-character
// passphrase and only honouring its first 72 bytes would be a lie the user
// never finds out about.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 72
)

// MaxEmailLen mirrors the CHECK on app_user.email.
const MaxEmailLen = 320

// inviteTTL is how long an invitation stays usable.
const inviteTTL = 7 * 24 * time.Hour

// loginFloor is the minimum wall-clock duration of a login attempt.
//
// A floor, not jitter. Jitter adds variance an attacker removes by averaging
// over repeated samples; a floor removes the signal outright, as long as it
// exceeds the real work in the worst case. bcrypt at DefaultCost is ~60–100 ms,
// so 250 ms leaves headroom on a loaded machine.
//
// Note that the floor also HIDES the dummy-hash burn below: with it applied,
// deleting that burn leaves both branches at 250 ms, so no end-to-end timing
// assertion can catch the regression. That is why burnDummyHash is a named
// function measured directly by TestLoginBurnsBcryptEvenForAnUnknownEmail —
// the two halves of the defence need separate tests.
const loginFloor = 250 * time.Millisecond

// dummyHash is what an unknown e-mail is compared against, so that a login for
// a non-existent account costs the same as one for a real account with a wrong
// password. Skipping bcrypt on the miss is the textbook enumeration oracle.
var dummyHash string

func init() {
	h, err := pwhash.Hash("foldex-dummy-password-for-constant-time-login")
	if err != nil {
		// Cannot happen: bcrypt only fails on a cost outside its valid range.
		panic("auth: cannot compute dummy hash: " + err.Error())
	}
	dummyHash = h
}

// burnDummyHash pays the same bcrypt cost the hit path pays.
//
// Named rather than inlined so the property can be measured directly: with the
// duration floor applied, its absence is invisible to any timing assertion.
func burnDummyHash(password string) {
	_ = pwhash.Verify(dummyHash, password)
}

// Handler serves /api/auth.
type Handler struct {
	repo    *Repository
	mw      *Middleware
	mailer  mailer.Mailer
	cookies CookieOptions
	ttl     SessionTTL
	logger  *slog.Logger
	baseURL string

	// cipher encrypts TOTP seeds at rest. Nil only when the auth stack is not
	// wired at all; every 2FA route checks it before use.
	cipher              *secrets.Cipher
	totpIssuer          string
	require2FAForAdmins bool

	loginByIP    *attemptlimit.Limiter
	loginByEmail *attemptlimit.Limiter
	bootstrapIP  *attemptlimit.Limiter
	inviteIP     *attemptlimit.Limiter
	pwResetIP    *attemptlimit.Limiter
	pwResetEmail *attemptlimit.Limiter
	// stepUpUser caps TOTP guesses on the SESSION-authenticated paths (disable,
	// regenerate). Verify2FA is bounded by auth_challenge.attempts, which does
	// not apply here because there is no challenge — without this the two
	// step-up endpoints accept unlimited codes.
	stepUpUser *attemptlimit.Limiter

	// features is echoed on /me so the SPA can render the right affordances
	// (e.g. "check the server log" instead of "check your inbox").
	features map[string]any
}

// HandlerConfig is the wiring internal/server hands in.
type HandlerConfig struct {
	Repo    *Repository
	MW      *Middleware
	Mailer  mailer.Mailer
	Cookies CookieOptions
	TTL     SessionTTL
	Logger  *slog.Logger
	// BaseURL is the public origin used to build invite links. It cannot be
	// derived from the request: Host and X-Forwarded-Host are attacker-supplied,
	// and a link built from them is a password-reset-poisoning primitive — the
	// mail goes to the real user but points at the attacker's host.
	BaseURL string

	// Cipher encrypts TOTP seeds at rest. Loaded via internal/pkg/keyfile with
	// AllowEphemeral=false, because a regenerated key would make every stored
	// seed undecryptable and lock every 2FA user out permanently.
	Cipher     *secrets.Cipher
	TOTPIssuer string
	// Require2FAForAdmins diverts an administrator without an authenticator
	// into mandatory enrollment instead of straight into a session.
	Require2FAForAdmins bool
}

func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		repo:    cfg.Repo,
		mw:      cfg.MW,
		mailer:  cfg.Mailer,
		cookies: cfg.Cookies,
		ttl:     cfg.TTL,
		logger:  cfg.Logger,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),

		cipher:              cfg.Cipher,
		totpIssuer:          cfg.TOTPIssuer,
		require2FAForAdmins: cfg.Require2FAForAdmins,

		loginByIP:    attemptlimit.New(20, 15*time.Minute),
		loginByEmail: attemptlimit.New(5, 15*time.Minute),
		bootstrapIP:  attemptlimit.New(5, time.Hour),
		inviteIP:     attemptlimit.New(20, time.Hour),
		// A reset request costs an e-mail to a third party, so the per-address
		// budget is tighter than the per-IP one: the abuse worth preventing is
		// mailbombing one victim, not making many requests.
		pwResetIP:    attemptlimit.New(10, time.Hour),
		pwResetEmail: attemptlimit.New(3, time.Hour),
		stepUpUser:   attemptlimit.New(5, 15*time.Minute),

		features: map[string]any{
			"google_oauth":   false, // PR4
			"two_factor":     cfg.Cipher != nil,
			"email_delivery": cfg.Mailer.Driver() == "smtp",
		},
	}
}

// Mount wires the public half of the auth surface. Everything here runs
// WITHOUT the Authenticate middleware; the routes that need a principal apply
// it individually, because most of this surface exists precisely to establish
// one.
func (h *Handler) Mount(r chi.Router) {
	r.Use(NoStore)

	r.Get("/bootstrap-status", h.BootstrapStatus)
	r.Post("/bootstrap", h.Bootstrap)
	r.Post("/login", h.Login)
	r.Post("/refresh", h.Refresh)
	r.Get("/invites/{token}", h.LookupInvite)
	r.Post("/invites/accept", h.AcceptInvite)

	r.Post("/password/forgot", h.ForgotPassword)
	r.Post("/password/reset", h.ResetPassword)
	// Unauthenticated: the confirmation link is followed from a mail client,
	// often on a device with no session. The 256-bit token is the credential.
	r.Post("/email/verify", h.VerifyEmail)

	// The second-factor routes authenticate with the PRE-AUTH cookie, not a
	// session — a session is exactly what they exist to produce.
	r.Post("/2fa/verify", h.Verify2FA)
	r.Post("/2fa/email", h.SendEmailOTP)

	// Enrollment is reachable BOTH from a live session (adding a factor in
	// settings) and from a pre-auth challenge (an admin forced to enrol before
	// the login completes). Optional resolves a principal when one exists and
	// lets the handler fall back to the challenge when it does not.
	r.With(h.mw.Optional).Post("/2fa/totp/start", h.StartTOTP)
	r.With(h.mw.Optional).Get("/2fa/totp/qr.png", h.TOTPQR)
	r.With(h.mw.Optional).Post("/2fa/totp/confirm", h.ConfirmTOTP)

	// /me resolves a principal when there is one but never rejects.
	r.With(h.mw.Optional).Get("/me", h.Me)
	// Logout must work on a half-dead session, so it reads the cookie directly
	// rather than going through Authenticate — a user whose access token just
	// expired still expects "sign out" to clear their cookies.
	r.Post("/logout", h.Logout)

	r.Group(func(pr chi.Router) {
		pr.Use(h.mw.Authenticate)
		pr.Post("/logout-all", h.LogoutAll)
		pr.Get("/sessions", h.Sessions)
		pr.Delete("/sessions/{id}", h.RevokeSession)
		pr.Post("/password/change", h.ChangePassword)
		pr.Post("/email/resend", h.SendEmailVerification)
		pr.Get("/2fa", h.TwoFactorStatus)
		pr.Post("/2fa/totp/disable", h.DisableTOTP)
		pr.Post("/2fa/recovery-codes/regenerate", h.RegenerateRecoveryCodes)
	})
}

// SweepLimiters evicts stale rate-limit entries from every in-memory bucket and
// returns how many keys were dropped.
//
// This MUST be driven by a ticker. The buckets are maps keyed by
// attacker-supplied input on unauthenticated endpoints — `login:em:<e-mail>`
// most of all — so without eviction every distinct address ever tried leaves a
// permanent entry and the throttle becomes a memory leak reachable pre-auth.
// The limiter documents that hazard; wiring the sweep is what makes the
// documentation true.
func (h *Handler) SweepLimiters(olderThan time.Duration) int {
	n := 0
	for _, l := range []*attemptlimit.Limiter{
		h.loginByIP, h.loginByEmail, h.bootstrapIP, h.inviteIP,
		h.pwResetIP, h.pwResetEmail, h.stepUpUser,
	} {
		n += l.Sweep(olderThan)
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────
// Bootstrap
// ─────────────────────────────────────────────────────────────────────

func (h *Handler) BootstrapStatus(w http.ResponseWriter, r *http.Request) {
	needs, err := h.repo.NeedsBootstrap(r.Context())
	if err != nil {
		h.logger.Error("bootstrap status", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"needs_bootstrap": needs})
}

type bootstrapInput struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// Bootstrap claims the placeholder admin created by migration 000017.
func (h *Handler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if until, ok := h.bootstrapIP.Begin("bootstrap:" + ip); !ok {
		writeRateLimited(w, until)
		return
	}
	committed := false
	defer func() {
		if !committed {
			h.bootstrapIP.Release("bootstrap:" + ip)
		}
	}()

	in, err := httperr.DecodeJSON[bootstrapInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := validateEmail(in.Email); err != nil {
		httperr.Write(w, err)
		return
	}
	if err := validatePassword(in.Password); err != nil {
		httperr.Write(w, err)
		return
	}

	user, err := h.repo.Bootstrap(r.Context(), in.Email, strings.TrimSpace(in.Name), in.Password)
	switch {
	case errors.Is(err, ErrAlreadySetUp):
		h.bootstrapIP.CommitFail("bootstrap:" + ip)
		committed = true
		httperr.Write(w, httperr.New(http.StatusConflict, "already_configured",
			"this instance already has an account"))
		return
	case errors.Is(err, ErrEmailTaken):
		h.bootstrapIP.CommitFail("bootstrap:" + ip)
		committed = true
		httperr.Write(w, httperr.New(http.StatusConflict, "email_taken", "e-mail already registered"))
		return
	case err != nil:
		h.logger.Error("bootstrap", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.bootstrapIP.CommitSuccess("bootstrap:" + ip)
	committed = true

	h.issueAndRespond(w, r, user)
}

// ─────────────────────────────────────────────────────────────────────
// Login / logout / refresh
// ─────────────────────────────────────────────────────────────────────

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login authenticates a password and issues a session.
//
// Every failure — unknown e-mail, wrong password, disabled account — produces
// the SAME 401 body, and the handler takes the same minimum time. A distinct
// `account_disabled` code would confirm that the address is registered, which
// is exactly the fact the anti-enumeration design refuses to leak.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	defer floorDuration(time.Now(), loginFloor)

	in, err := httperr.DecodeJSON[loginInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}

	ip := clientIP(r)
	ipKey := "login:ip:" + ip
	// The e-mail bucket is keyed by the NORMALIZED address rather than the raw
	// input, so `A@x.com` and `a@x.com ` share one budget instead of giving an
	// attacker a fresh 5 attempts per capitalization.
	emailKey := "login:em:" + NormalizeEmail(in.Email)

	if until, ok := h.loginByIP.Begin(ipKey); !ok {
		writeRateLimited(w, until)
		return
	}
	if until, ok := h.loginByEmail.Begin(emailKey); !ok {
		h.loginByIP.Release(ipKey)
		writeRateLimited(w, until)
		return
	}

	user, found, verr := h.repo.verifyPassword(r.Context(), in.Email, in.Password)
	if !found {
		burnDummyHash(in.Password)
	}
	if verr != nil && !errors.Is(verr, ErrBadCredentials) {
		h.loginByIP.Release(ipKey)
		h.loginByEmail.Release(emailKey)
		h.logger.Error("login", "err", verr)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	// A pending or disabled account fails exactly like a wrong password. The
	// counters increment for an unknown address too — not incrementing is
	// itself an oracle, since the attacker learns which addresses can be
	// locked out.
	if !found || verr != nil || user.Status != StatusActive {
		h.loginByIP.CommitFail(ipKey)
		h.loginByEmail.CommitFail(emailKey)
		httperr.Write(w, errInvalidCredentials())
		return
	}

	h.loginByIP.CommitSuccess(ipKey)
	h.loginByEmail.CommitSuccess(emailKey)

	// The password is proven. Whether that IS the login depends on the account's
	// second factor and on the admin policy.
	h.completeLogin(w, r, user, false)
}

// Logout revokes the current session and always answers 204.
//
// It never fails. Signing out is a user telling the product to forget them; the
// only wrong outcome is one where their cookies survive. So the cookies are
// cleared unconditionally, before the (best-effort) revocation is even
// attempted.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if raw := cookieValue(r, CookieAccess); raw != "" {
		h.repo.RevokeByAccessToken(r.Context(), raw, ReasonLogout)
	}
	if p, ok := authctx.FromContext(r.Context()); ok && p.SessionID != 0 {
		h.mw.forgetTouch(p.SessionID)
	}
	h.cookies.ClearSession(w)
	w.WriteHeader(http.StatusNoContent)
}

// LogoutAll revokes every session for the caller.
func (h *Handler) LogoutAll(w http.ResponseWriter, r *http.Request) {
	uid := authctx.MustUser(r.Context())
	if err := h.repo.RevokeAllForUser(r.Context(), uid, ReasonLogoutAll); err != nil {
		h.logger.Error("logout all", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.cookies.ClearSession(w)
	w.WriteHeader(http.StatusNoContent)
}

// Refresh rotates the session's token triple.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	raw := cookieValue(r, CookieRefresh)
	if raw == "" {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "no_session", "no refresh token"))
		return
	}
	res, err := h.repo.Rotate(r.Context(), raw, h.ttl, clientIP(r), r.UserAgent())
	switch {
	case errors.Is(err, ErrSessionReuse):
		// The family is already dead by the time this returns. Warn the owner
		// out-of-band: if the token really was stolen, this is the only signal
		// they will get.
		h.notifyReuse(res.UserID)
		h.cookies.ClearSession(w)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "session_revoked",
			"session was revoked; sign in again"))
		return
	case errors.Is(err, ErrUserNotActive):
		h.cookies.ClearSession(w)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "account_inactive", "account is not active"))
		return
	case errors.Is(err, ErrSessionInvalid):
		h.cookies.ClearSession(w)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "session_expired", "session expired"))
		return
	case err != nil:
		h.logger.Error("refresh", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if res.Replayed {
		h.logger.Info("refresh served from grace window", "session_id", res.Session)
	}
	h.cookies.SetSession(w, res.Tokens)

	user, err := h.repo.GetUser(r.Context(), res.UserID)
	if err != nil {
		h.logger.Error("refresh load user", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, h.authenticatedPayload(user, res.Tokens.CSRF))
}

// notifyReuse tells the owner their sessions were killed. Fire-and-forget on a
// detached context: the response must not wait on SMTP, and the request's own
// context is cancelled the moment it returns.
func (h *Handler) notifyReuse(uid authctx.UserID) {
	h.logger.Warn("refresh token reuse detected — session family revoked", "user_id", int64(uid))
	if uid == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		email, err := h.repo.EmailForUser(ctx, uid)
		if err != nil {
			return
		}
		if err := h.mailer.Send(ctx, mailer.SessionRevokedMessage(email)); err != nil {
			h.logger.Error("reuse notification", "err", err)
		}
	}()
}

// ─────────────────────────────────────────────────────────────────────
// Me / sessions / password
// ─────────────────────────────────────────────────────────────────────

// Me is ALWAYS 200. See SDD §4.1 — a 401 here would recurse through the SPA's
// refresh interceptor on every cold boot.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	p, ok := authctx.FromContext(r.Context())
	if !ok {
		needs, err := h.repo.NeedsBootstrap(r.Context())
		if err != nil {
			h.logger.Error("me bootstrap check", "err", err)
			httperr.Write(w, httperr.ErrInternal)
			return
		}
		status := "anonymous"
		if needs {
			status = "setup_required"
		}
		httperr.JSON(w, http.StatusOK, map[string]any{
			"status":   status,
			"features": h.features,
		})
		return
	}
	user, err := h.repo.GetUser(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("me", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// The CSRF token echoed here is the COOKIE value the browser already holds,
	// not a new one — /me is a GET and must not rotate credentials. The SPA
	// reads it from the cookie; this field exists so a client that cannot read
	// cookies (tests, curl) can still drive the API.
	httperr.JSON(w, http.StatusOK, h.authenticatedPayload(user, cookieValue(r, CookieCSRF)))
}

func (h *Handler) authenticatedPayload(u User, csrf string) map[string]any {
	return map[string]any{
		"status":     "authenticated",
		"user":       u,
		"csrf_token": csrf,
		"features":   h.features,
	}
}

func (h *Handler) Sessions(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	list, err := h.repo.ListSessions(r.Context(), p.UserID, p.SessionID)
	if err != nil {
		h.logger.Error("list sessions", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (h *Handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := h.repo.RevokeSession(r.Context(), p.UserID, id, ReasonLogout); err != nil {
		httperr.Write(w, err)
		return
	}
	h.mw.forgetTouch(id)
	// Revoking the session you are currently using is a logout; clear the
	// cookies so the SPA does not keep a dead access token around.
	if id == p.SessionID {
		h.cookies.ClearSession(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

type changePasswordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword requires the current password and then revokes every OTHER
// session.
//
// Proving possession of the current credential is what stops a stolen session
// from being upgraded into permanent account takeover: without it, an attacker
// with a hijacked cookie could set a new password and lock the real owner out.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	in, err := httperr.DecodeJSON[changePasswordInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := validatePassword(in.NewPassword); err != nil {
		httperr.Write(w, err)
		return
	}
	switch err := h.repo.VerifyUserPassword(r.Context(), p.UserID, in.CurrentPassword); {
	case errors.Is(err, ErrBadCredentials), errors.Is(err, ErrPasswordMissing):
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "invalid_credentials",
			"current password is incorrect"))
		return
	case err != nil:
		h.logger.Error("change password verify", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if err := h.repo.SetPassword(r.Context(), p.UserID, in.NewPassword); err != nil {
		h.logger.Error("change password", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// Keep the caller signed in on THIS device and drop the rest — a password
	// change is how a user reacts to suspected compromise, so every other
	// session has to die, but signing them out of the browser they are actively
	// using would be hostile.
	if err := h.repo.RevokeAllExcept(r.Context(), p.UserID, p.SessionID, ReasonPasswordChanged); err != nil {
		h.logger.Error("change password revoke others", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────────────────────────────────────────────────
// Invites
// ─────────────────────────────────────────────────────────────────────

// LookupInvite resolves a raw token so the accept screen can show the address
// it is bound to. 404 for anything unusable — expired, revoked, already used —
// so the endpoint reveals nothing beyond "this exact token works or it does
// not".
func (h *Handler) LookupInvite(w http.ResponseWriter, r *http.Request) {
	inv, err := h.repo.LookupInvite(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{
		"email":      inv.Email,
		"role":       inv.Role,
		"expires_at": inv.ExpiresAt,
	})
}

type acceptInviteInput struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// AcceptInvite creates the account and signs the new user straight in.
func (h *Handler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	key := "invite:" + ip
	if until, ok := h.inviteIP.Begin(key); !ok {
		writeRateLimited(w, until)
		return
	}
	settled := false
	defer func() {
		if !settled {
			h.inviteIP.Release(key)
		}
	}()

	in, err := httperr.DecodeJSON[acceptInviteInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := validatePassword(in.Password); err != nil {
		httperr.Write(w, err)
		return
	}

	user, err := h.repo.AcceptInvite(r.Context(), in.Token, strings.TrimSpace(in.Name), in.Password)
	switch {
	case errors.Is(err, ErrInviteInvalid):
		h.inviteIP.CommitFail(key)
		settled = true
		httperr.Write(w, httperr.New(http.StatusNotFound, "invite_invalid",
			"this invitation is no longer valid"))
		return
	case errors.Is(err, ErrEmailTaken):
		h.inviteIP.CommitFail(key)
		settled = true
		httperr.Write(w, httperr.New(http.StatusConflict, "email_taken", "e-mail already registered"))
		return
	case err != nil:
		h.logger.Error("accept invite", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.inviteIP.CommitSuccess(key)
	settled = true

	h.issueAndRespond(w, r, user)
}

// ─────────────────────────────────────────────────────────────────────
// Shared helpers
// ─────────────────────────────────────────────────────────────────────

// issueAndRespond mints a session for user and writes the authenticated
// payload. Every successful credential path funnels through here, so there is
// exactly one place that decides what "signed in" means on the wire.
func (h *Handler) issueAndRespond(w http.ResponseWriter, r *http.Request, user User) {
	tok, _, err := h.repo.IssueSession(r.Context(), user.ID, h.ttl, clientIP(r), r.UserAgent())
	if err != nil {
		h.logger.Error("issue session", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.cookies.SetSession(w, tok)
	httperr.JSON(w, http.StatusOK, h.authenticatedPayload(user, tok.CSRF))
}

// errInvalidCredentials is the single failure shape the login path emits.
func errInvalidCredentials() error {
	return httperr.New(http.StatusUnauthorized, "invalid_credentials", "invalid e-mail or password")
}

func writeRateLimited(w http.ResponseWriter, until time.Time) {
	retry := int(time.Until(until).Seconds())
	if retry < 1 {
		retry = 1
	}
	w.Header().Set("Retry-After", fmt.Sprint(retry))
	httperr.Write(w, httperr.New(http.StatusTooManyRequests, "too_many_attempts",
		"too many attempts; try again later"))
}

// floorDuration sleeps out the remainder of d since start. Deferred by the
// login handler so every exit path — including the early returns — pays it.
func floorDuration(start time.Time, d time.Duration) {
	if rem := d - time.Since(start); rem > 0 {
		time.Sleep(rem)
	}
}

func validatePassword(p string) error {
	if utf8.RuneCountInString(p) < MinPasswordLen {
		return httperr.New(http.StatusBadRequest, "password_too_short",
			fmt.Sprintf("password must be at least %d characters", MinPasswordLen))
	}
	// Measured in BYTES, because that is the unit bcrypt truncates in.
	if len(p) > MaxPasswordLen {
		return httperr.New(http.StatusBadRequest, "password_too_long",
			fmt.Sprintf("password must be at most %d bytes", MaxPasswordLen))
	}
	return nil
}

func validateEmail(email string) error {
	e := strings.TrimSpace(email)
	if len(e) < 3 || len(e) > MaxEmailLen {
		return httperr.New(http.StatusBadRequest, "invalid_email", "invalid e-mail address")
	}
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 || strings.ContainsAny(e, " \t\r\n") {
		return httperr.New(http.StatusBadRequest, "invalid_email", "invalid e-mail address")
	}
	if !strings.Contains(e[at+1:], ".") {
		return httperr.New(http.StatusBadRequest, "invalid_email", "invalid e-mail address")
	}
	return nil
}

// clientIP returns the peer address for rate-limit keys.
//
// It reads RemoteAddr, which server.trustedProxyRealIP has rewritten from
// X-Forwarded-For only when the request arrived from a peer in
// TRUSTED_PROXY_IPS. (That middleware replaced chi's middleware.RealIP, which
// honoured the header from anyone.) Our middleware writes a bare IP with no
// port, so SplitHostPort errors and the fallback below returns it unchanged.
//
// The login path ALSO keys a bucket by e-mail, and that redundancy is the
// point: if the IP key is ever wrong — a misconfigured proxy list, a header we
// were talked into believing — the e-mail bucket still caps guesses against any
// single account.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Package oauthgoogle speaks the Google half of the Authorization Code + PKCE
// flow: it builds the redirect, exchanges the code, and reads the profile.
//
// It deliberately knows nothing about foldex accounts. Deciding which app_user
// a Google `sub` maps to — and refusing to auto-provision one — is policy, and
// policy lives in internal/auth where the anti-takeover rules are tested
// together. This package's whole job is "talk to Google, correctly".
package oauthgoogle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Google's endpoints. Constants, never configuration: a redirect or token
// endpoint that an operator can point elsewhere is an open redirect and a
// credential-exfiltration channel wearing a config knob.
const (
	authEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenEndpoint    = "https://oauth2.googleapis.com/token" // #nosec G101 -- Google's token endpoint URL, not a credential
	userInfoEndpoint = "https://openidconnect.googleapis.com/v1/userinfo"
)

// scopes is the minimum that identifies a person. `profile` is included only
// for the display name shown in the account menu; foldex asks for no Drive,
// no contacts, and no offline access — there is no refresh token to steal
// because none is ever requested.
const scopes = "openid email profile"

// maxBodyBytes caps what is read from Google. Google will not send megabytes,
// but the cap is what makes that a property of this code rather than a
// property of the remote server's good behaviour.
const maxBodyBytes = 64 << 10

// ErrDisabled is returned when OAuth is called without client credentials.
var ErrDisabled = errors.New("oauthgoogle: not configured")

// ErrProvider marks any failure on Google's side — network, non-2xx, malformed
// JSON. Collapsed into one error on purpose: the handler turns it into a single
// opaque response, and distinguishing "Google is down" from "that code was
// already used" only helps whoever is probing.
var ErrProvider = errors.New("oauthgoogle: provider request failed")

// Config is what an operator supplies.
type Config struct {
	ClientID     string
	ClientSecret string
	// RedirectURL must match a URI registered in the Google Cloud console
	// exactly. It is built from AUTH_PUBLIC_URL rather than from the request,
	// for the same reason invite links are: Host and X-Forwarded-Host are
	// attacker-supplied.
	RedirectURL string
	// HTTPClient is optional; tests inject one pointed at a stub server.
	HTTPClient *http.Client
}

// Provider is a configured Google client.
type Provider struct {
	clientID     string
	clientSecret string
	redirectURL  string
	http         *http.Client

	// Overridable only from tests in this package.
	authEndpoint, tokenEndpoint, userInfoEndpoint string
}

// New builds a provider. A Provider with missing credentials is still usable —
// it reports Enabled() == false and every call returns ErrDisabled — so the
// server can wire the routes unconditionally and let one check decide.
func New(cfg Config) *Provider {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Provider{
		clientID:         strings.TrimSpace(cfg.ClientID),
		clientSecret:     strings.TrimSpace(cfg.ClientSecret),
		redirectURL:      strings.TrimSpace(cfg.RedirectURL),
		http:             hc,
		authEndpoint:     authEndpoint,
		tokenEndpoint:    tokenEndpoint,
		userInfoEndpoint: userInfoEndpoint,
	}
}

// Enabled reports whether the provider has everything it needs. All three
// values are required: a client id without a redirect URL produces a redirect
// Google refuses, which surfaces as an inscrutable error page rather than as
// "OAuth is off".
func (p *Provider) Enabled() bool {
	return p.clientID != "" && p.clientSecret != "" && p.redirectURL != ""
}

// ─────────────────────────────────────────────────────────────────────
// PKCE
// ─────────────────────────────────────────────────────────────────────

// PKCE is one challenge/verifier pair.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE mints a verifier and its S256 challenge.
//
// The verifier is 64 random bytes rendered base64url (86 characters, inside
// RFC 7636's 43–128 range). Only S256 is ever produced: the `plain` method
// puts the verifier itself in the redirect, which defeats the entire purpose
// on a device where another app can observe the URL.
func NewPKCE() (PKCE, error) {
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, fmt.Errorf("oauthgoogle: rand: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────
// The redirect
// ─────────────────────────────────────────────────────────────────────

// AuthCodeURL builds the URL the browser is sent to.
//
// `prompt=select_account` is not cosmetic. Without it, a browser already signed
// into one Google account completes the flow with that account silently — so a
// user trying to link a second identity, or one on a shared machine, lands in
// the wrong account with no visible decision point. The failure is worst
// exactly where it matters: linking.
func (p *Provider) AuthCodeURL(state, challenge string) (string, error) {
	if !p.Enabled() {
		return "", ErrDisabled
	}
	q := url.Values{
		"client_id":             {p.clientID},
		"redirect_uri":          {p.redirectURL},
		"response_type":         {"code"},
		"scope":                 {scopes},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
		// No offline access: foldex never holds a Google refresh token, so a
		// database dump cannot be turned into standing access to a mailbox.
		"access_type": {"online"},
	}
	return p.authEndpoint + "?" + q.Encode(), nil
}

// ─────────────────────────────────────────────────────────────────────
// Code exchange and profile
// ─────────────────────────────────────────────────────────────────────

// Exchange trades the authorization code for an access token.
//
// The id_token that comes back alongside it is deliberately ignored — see
// UserInfo.
func (p *Provider) Exchange(ctx context.Context, code, verifier string) (string, error) {
	if !p.Enabled() {
		return "", ErrDisabled
	}
	form := url.Values{
		"code":          {code},
		"client_id":     {p.clientID},
		"client_secret": {p.clientSecret},
		"redirect_uri":  {p.redirectURL},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrProvider, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := p.do(req, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("%w: empty access_token", ErrProvider)
	}
	return out.AccessToken, nil
}

// UserInfo is the subset of the OIDC profile foldex uses.
//
// Plain fields, no custom types: this struct crosses a package boundary and
// gets constructed by test doubles, so a field whose type is unexported would
// make it unbuildable from anywhere else. The JSON quirks live in
// UnmarshalJSON instead.
type UserInfo struct {
	// Subject is Google's immutable account identifier. It is the ONLY field
	// that may resolve a login: an e-mail address can be changed inside a
	// Google account, and treating it as the key would let that change move a
	// foldex account with it.
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// UnmarshalJSON decodes the OIDC profile, tolerating a string `email_verified`.
//
// Some providers render that claim as "true" rather than true. Accepting both
// is fine; what must never happen is a shape that is not a real `true`
// decoding as verified — that field is what stands between "an address Google
// vouches for" and "an address someone typed into a profile", and a lenient
// parser would let an unverified one drive account conversion. So the check is
// fail-closed: anything else, including an absent field, is false.
func (u *UserInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		Subject       string          `json:"sub"`
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
		Name          string          `json:"name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	verified := strings.TrimSpace(string(raw.EmailVerified))
	*u = UserInfo{
		Subject:       raw.Subject,
		Email:         raw.Email,
		EmailVerified: verified == "true" || verified == `"true"`,
		Name:          raw.Name,
	}
	return nil
}

// UserInfo reads the profile from the OIDC userinfo endpoint.
//
// The id_token returned by Exchange is NOT parsed. foldex has no JWT library as
// a direct dependency, and adopting one means taking on JWKS fetching and
// rotation, `alg` confusion, and aud/iss/exp validation — a whole CVE class —
// to save one HTTPS call on a flow that happens about once a month.
func (p *Provider) UserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	if !p.Enabled() {
		return UserInfo{}, ErrDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.userInfoEndpoint, nil)
	if err != nil {
		return UserInfo{}, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	var info UserInfo
	if err := p.do(req, &info); err != nil {
		return UserInfo{}, err
	}
	if info.Subject == "" || info.Email == "" {
		return UserInfo{}, fmt.Errorf("%w: profile is missing sub or email", ErrProvider)
	}
	return info, nil
}

// do performs the request and decodes a JSON body, capping how much is read.
func (p *Provider) do(req *http.Request, out any) error {
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProvider, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProvider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The body is NOT included. It can carry the client_secret back in an
		// error echo, and this error is logged.
		return fmt.Errorf("%w: status %d", ErrProvider, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: %v", ErrProvider, err)
	}
	return nil
}

//go:build integration

// Package auth_test drives the real HTTP surface against a real Postgres.
//
// Everything here goes through chi and the actual handlers rather than calling
// repository methods directly, because most of what PR2 promises lives in the
// seam between them: cookie attributes, the CSRF header check, the constant
// login response, the rate-limit budget. A repository-level test would pass
// while every one of those was broken.
package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/auth"
	"foldex/internal/mailer"
	"foldex/internal/mailoutbox"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/pkg/secrets"
	"foldex/internal/policy"
	"foldex/internal/testdb"
)

// TestMain owns the lifetime of the package's shared Postgres container.
//
// It has to be here rather than in a t.Cleanup because os.Exit skips deferred
// work and a cleanup hung off whichever test ran first would tear the database
// down while the rest of the package still needed it. The Makefile disables
// testcontainers' reaper, so nothing else would collect it.
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

// ─────────────────────────────────────────────────────────────────────
// Harness
// ─────────────────────────────────────────────────────────────────────

// captureMailer records what would have been sent.
//
// It is mutex-guarded because dispatcher workers send concurrently with the
// test goroutine polling for a message.
type captureMailer struct {
	mu sync.Mutex
	// driver is what Driver() reports. It matters: the e-mail second factor is
	// only offered when real SMTP is configured, because the `log` driver prints
	// the message body to stdout and a second factor in the container log is not
	// a second factor. Tests that exercise the OTP path set this to "smtp".
	driver string
	sent   []mailer.Message
	err    error
}

func (c *captureMailer) Driver() string {
	if c.driver == "" {
		return "log"
	}
	return c.driver
}
func (c *captureMailer) Send(_ context.Context, m mailer.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.sent = append(c.sent, m)
	return nil
}

// all returns a snapshot. Callers must not hold on to the slice header across
// another send.
func (c *captureMailer) all() []mailer.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]mailer.Message(nil), c.sent...)
}

func (c *captureMailer) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = nil
	c.err = nil
}

func (c *captureMailer) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// waitFor blocks until SOME message addressed to `to` has been delivered, and
// returns the most recent one.
//
// It is NOT a barrier for a second message to the same address. It is satisfied
// the moment any match exists, so a test that sends twice and calls it twice
// gets the first message back both times and proceeds while the second is still
// in flight. Use drainMail when the count matters — it queues a marker behind
// the work and waits for the marker, which is what "everything before this has
// been delivered" actually requires.
//
// Scanning backwards is what makes the "most recent" in the first line true;
// the previous version returned the first match while claiming otherwise, and
// that gap is what hid the trap above.
func (c *captureMailer) waitFor(t *testing.T, to string) mailer.Message {
	t.Helper()
	var found mailer.Message
	require.Eventually(t, func() bool {
		all := c.all()
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].To == to {
				found = all[i]
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "no mail delivered to %s", to)
	return found
}

// waitForMatching is waitFor with a predicate on the body.
//
// Returning "the last message to this address" is not good enough once a test
// performs several operations: the outbox relay delivers asynchronously, so a
// notification triggered by the PREVIOUS step (a recovery code was spent, a
// session was revoked) can land after the reset and win the backwards scan. The
// caller then parses a body that was never meant to carry what it is looking
// for, and the failure names the wrong message entirely.
func (c *captureMailer) waitForMatching(t *testing.T, to string, want func(string) bool) mailer.Message {
	t.Helper()
	var found mailer.Message
	require.Eventually(t, func() bool {
		all := c.all()
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].To == to && want(all[i].Text) {
				found = all[i]
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "no matching mail delivered to %s", to)
	return found
}

// waitForCode waits for a message that actually carries a six-digit code.
func (c *captureMailer) waitForCode(t *testing.T, to string) string {
	t.Helper()
	msg := c.waitForMatching(t, to, func(body string) bool {
		return sixDigits.MatchString(body)
	})
	return extractSixDigits(t, msg.Text)
}

type harness struct {
	pool    *pgxpool.Pool
	router  http.Handler
	mail    *captureMailer
	relay   *mailoutbox.Relay
	repo    *auth.Repository
	cipher  *secrets.Cipher
	codeMAC *auth.CodeMAC
}

const testBaseURL = "https://foldex.test"

func testSessionTTL() auth.SessionTTL {
	return auth.SessionTTL{
		Access: 15 * time.Minute, Refresh: 30 * 24 * time.Hour,
		Absolute: 90 * 24 * time.Hour, Grace: 10 * time.Second,
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	return newHarnessOn(t, pool)
}

// newHarnessOn builds a router over an existing pool, so tests that need a
// FRESH rate-limit budget (the limiters are per-Handler, in memory) can rebuild
// the router without paying for another container.
func newHarnessOn(t *testing.T, pool *pgxpool.Pool) *harness {
	return newHarnessWith(t, pool, harnessOpts{})
}

// harnessOpts turns on the parts of the stack that are off by default, so the
// pre-2FA tests keep exercising the pre-2FA behaviour and only the tests that
// mean to opt in pay for it.
type harnessOpts struct {
	TwoFactor           bool
	Require2FAForAdmins bool
	// SMTP makes the fake mailer report the "smtp" driver, which is what
	// unlocks the e-mail second factor.
	SMTP bool
	// CipherSeed varies the encryption key. Non-zero stands up a stack that
	// CANNOT read seeds another harness wrote.
	CipherSeed byte
	// Google injects an OAuth provider double. Nil leaves the routes mounted
	// but reporting "not configured", which is what an instance without client
	// credentials does.
	Google                auth.GoogleProvider
	AfterTOTPVerification func(context.Context, authctx.UserID, auth.TOTPProof)
	// Policy mounts /api/admin/policy and wires the owner-configurable rules
	// into the handler. Off by default so the tests written before ADR-35 keep
	// exercising the compiled-in floors.
	Policy bool
}

// testCipher is a FIXED key, so a test can assert that a seed encrypted in one
// harness is readable by another — which is the property a rotated
// AUTH_ENCRYPTION_KEY would break.
func testCipher(t *testing.T) *secrets.Cipher { return testCipherSeeded(t, 0) }

// testCipherSeeded builds a DIFFERENT key per seed, so a test can stand up a
// second stack over the same database with the wrong key — which is exactly
// what a regenerated key file produces on the next boot.
func testCipherSeeded(t *testing.T, seed byte) *secrets.Cipher {
	t.Helper()
	key := testAuthKey(seed)
	c, err := secrets.NewCipher(key)
	require.NoError(t, err)
	return c
}

func testAuthKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i*7)
	}
	return key
}

func newHarnessWith(t *testing.T, pool *pgxpool.Pool, opts harnessOpts) *harness {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mail := &captureMailer{}
	if opts.SMTP {
		mail.driver = "smtp"
	}
	// One relay worker gives tests a deterministic FIFO drain barrier, which is
	// what drainMail's marker relies on. Relay concurrency itself is covered in
	// internal/mailoutbox.
	const workers = 1
	// The outbox cipher is built unconditionally, unlike cfg.Cipher: every
	// credential-minting path writes a queued row now, not just the 2FA ones.
	outboxCipher := testCipherSeeded(t, opts.CipherSeed)
	outbox, err := mailoutbox.New(outboxCipher)
	require.NoError(t, err)
	repo := auth.NewRepository(pool, auth.WithOutbox(outbox))
	relay := mailoutbox.NewRelay(mailoutbox.NewRepository(pool),
		mailoutbox.NewInprocSink(outbox, mail),
		// Polled far faster than production: the assertions wait on delivery,
		// and a one-second tick would put every mail test near its deadline.
		mailoutbox.Options{PollInterval: 5 * time.Millisecond, Workers: workers}, logger)
	relay.Start(context.Background())
	t.Cleanup(relay.Stop)
	cookies := auth.CookieOptions{Secure: true}
	mw := auth.NewMiddleware(repo, cookies, logger, opts.Require2FAForAdmins)

	cfg := auth.HandlerConfig{
		Repo: repo, MW: mw, Mailer: mail, Cookies: cookies,
		TTL: testSessionTTL(), Logger: logger, BaseURL: testBaseURL,
		Require2FAForAdmins: opts.Require2FAForAdmins,
		Google:              opts.Google,
	}
	var policyRepo *policy.Repository
	if opts.Policy {
		policyRepo = policy.NewRepository(pool)
		cfg.Policy = policyRepo
	}
	if opts.TwoFactor || opts.Require2FAForAdmins {
		key := testAuthKey(opts.CipherSeed)
		cfg.Cipher = testCipherSeeded(t, opts.CipherSeed)
		var err error
		cfg.CodeMAC, err = auth.NewCodeMAC(key)
		require.NoError(t, err)
		cfg.TOTPIssuer = "Foldex (test)"
	}
	h := auth.NewHandler(cfg)
	auth.SetTOTPVerificationHookForTest(h, opts.AfterTOTPVerification)
	admin := auth.NewAdminHandler(repo, mail, logger, testBaseURL)

	r := chi.NewRouter()
	r.Route("/api", func(api chi.Router) {
		api.Route("/auth", h.Mount)
		api.Group(func(pr chi.Router) {
			pr.Use(mw.Authenticate)
			pr.Route("/admin", func(ar chi.Router) {
				// Same order as internal/server: the role gate answers 404
				// first, so a non-admin cannot learn the surface exists, and
				// the token gate answers 403 only to an admin who presents a
				// bearer credential.
				ar.Use(mw.RequireAdmin)
				ar.Use(mw.RejectAPIToken)
				admin.Mount(ar)
				if policyRepo != nil {
					ar.Route("/policy", policy.NewHandler(
						policyRepo, logger, admin.AuditPolicyChange).Mount)
				}
			})
			// A stand-in for the content surface, so tests can assert that a
			// pre-session credential does not reach data endpoints.
			pr.Get("/links", func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"uid":%d}`, int64(authctx.MustUser(r.Context())))
			})
			// Carries the same write gate internal/server mounts on the real
			// content groups, so a viewer's refusal is exercised here rather
			// than only asserted about the middleware in isolation.
			pr.With(authgate.RequireWrite(authctx.PermContentWrite)).
				Post("/links", func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusCreated)
				})
		})
	})
	return &harness{
		pool: pool, router: r, mail: mail, relay: relay, repo: repo,
		cipher: cfg.Cipher, codeMAC: cfg.CodeMAC,
	}
}

const mailDrainAddress = "auth-mail-drain@foldex.test"

// drainMail waits until every message queued before the marker has been
// delivered, then returns those messages without the marker.
//
// The marker rides the same queue as everything else, and the relay's single
// test worker keeps the order: once the marker has been sent, nothing enqueued
// before it is still pending.
func (h *harness) drainMail(t *testing.T) []mailer.Message {
	t.Helper()
	require.NoError(t, h.repo.EnqueueMail(context.Background(),
		mailer.SessionRevokedMessage(mailDrainAddress), "en"))
	h.mail.waitFor(t, mailDrainAddress)

	h.mail.mu.Lock()
	defer h.mail.mu.Unlock()
	var sent []mailer.Message
	for _, msg := range h.mail.sent {
		if msg.To != mailDrainAddress {
			sent = append(sent, msg)
		}
	}
	h.mail.sent = nil
	return sent
}

// client is a tiny cookie-jar HTTP client over the in-process router.
type client struct {
	t       *testing.T
	h       *harness
	cookies map[string]string
}

type delayedResponseWriter struct {
	*httptest.ResponseRecorder
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *delayedResponseWriter) WriteHeader(status int) {
	w.once.Do(func() {
		close(w.reached)
		<-w.release
	})
	w.ResponseRecorder.WriteHeader(status)
}

func (h *harness) client(t *testing.T) *client {
	return &client{t: t, h: h, cookies: map[string]string{}}
}

func clientOnHarness(t *testing.T, h *harness, from *client) *client {
	t.Helper()
	c := h.client(t)
	for name, value := range from.cookies {
		c.cookies[name] = value
	}
	return c
}

func (c *client) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(c.t, err)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:1234"
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	// The SPA reads fx_csrf and echoes it. Mirroring that here means the CSRF
	// tests have to deliberately break the header to see a 403, rather than
	// passing because nobody ever sent one.
	if csrf, ok := c.cookies[auth.CookieCSRF]; ok && csrf != "" {
		req.Header.Set(auth.CSRFHeader, csrf)
	}
	rec := httptest.NewRecorder()
	c.h.router.ServeHTTP(rec, req)
	c.absorb(rec)
	return rec
}

// doRaw skips the automatic CSRF header, for the tests that need to observe a
// request without one.
func (c *client) doRaw(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:1234"
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.h.router.ServeHTTP(rec, req)
	c.absorb(rec)
	return rec
}

func (c *client) absorb(rec *httptest.ResponseRecorder) {
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
			continue
		}
		c.cookies[ck.Name] = ck.Value
	}
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), "body: %s", rec.Body.String())
	return out
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := decode(t, rec)
	e, ok := body["error"].(map[string]any)
	require.True(t, ok, "expected an error envelope, got: %s", rec.Body.String())
	return e["code"].(string)
}

func waitForBlockedSQL(t *testing.T, pool *pgxpool.Pool, fragment string) {
	waitForBlockedSQLCount(t, pool, fragment, 1)
}

func waitForBlockedSQLCount(t *testing.T, pool *pgxpool.Pool, fragment string, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var blocked int
		err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE '%' || $1 || '%'`, fragment).Scan(&blocked)
		return err == nil && blocked >= want
	}, 3*time.Second, 10*time.Millisecond,
		"%d queries did not block on the expected row lock: %s", want, fragment)
}

// bootstrapAdmin claims the placeholder admin and returns a signed-in client.
func (h *harness) bootstrapAdmin(t *testing.T, email, password string) *client {
	t.Helper()
	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": email, "name": "Admin", "password": password,
	})
	require.Equal(t, http.StatusOK, rec.Code, "bootstrap failed: %s", rec.Body.String())
	return c
}

func cookieByName(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// Bootstrap
// ─────────────────────────────────────────────────────────────────────

func TestBootstrap_ClaimsThePlaceholderAndSignsIn(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)

	rec := c.do(http.MethodGet, "/api/auth/bootstrap-status", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, true, decode(t, rec)["needs_bootstrap"])

	rec = c.do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "admin@example.com", "name": "Ana", "password": "correct horse battery",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := decode(t, rec)
	assert.Equal(t, "authenticated", body["status"])
	user := body["user"].(map[string]any)
	assert.Equal(t, "admin@example.com", user["email"])
	// Owner, not admin: whoever completes setup holds the instance (ADR-33).
	assert.Equal(t, "owner", user["role"])
	assert.Equal(t, "active", user["status"])

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user`).Scan(&n))
	assert.Equal(t, 1, n)

	rec = c.do(http.MethodGet, "/api/auth/bootstrap-status", nil)
	assert.Equal(t, false, decode(t, rec)["needs_bootstrap"])
}

// The upgrade path, and the reason migration 000017 inserts a placeholder at
// all: on an existing single-user install every pre-migration row was adopted
// by that placeholder, so bootstrap MUST claim it rather than insert a second
// account. Inserting instead would leave the new admin staring at an empty
// library while all their data sat under an unreachable pending row.
func TestBootstrap_ClaimingThePlaceholderAdoptsExistingContent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	var placeholder int64
	require.NoError(t, h.pool.QueryRow(ctx, `
		INSERT INTO app_user (email, email_normalized, name, role, status, password_hash)
		VALUES ('admin@foldex.local', 'admin@foldex.local', 'Administrator', 'admin', 'pending', NULL)
		RETURNING id`).Scan(&placeholder))
	_, err := h.pool.Exec(ctx,
		`INSERT INTO link (user_id, url, title, slug) VALUES ($1, 'https://old.test', 'Old', 'old')`,
		placeholder)
	require.NoError(t, err)

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "real@example.com", "name": "Ana", "password": "correct horse battery",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	claimed := int64(decode(t, rec)["user"].(map[string]any)["id"].(float64))
	assert.Equal(t, placeholder, claimed, "bootstrap must claim the placeholder row, not insert a new one")

	var owner int64
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT user_id FROM link WHERE slug = 'old'`).Scan(&owner))
	assert.Equal(t, placeholder, owner, "the pre-existing content must belong to the new admin")

	var n int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM app_user`).Scan(&n))
	assert.Equal(t, 1, n)
}

// The fallback path. A database with no placeholder at all — restored from a
// dump, or one where the row was deleted by hand — must still be recoverable
// through the setup screen rather than requiring direct SQL.
func TestBootstrap_WorksWithNoPlaceholderRow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	var n int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM app_user`).Scan(&n))
	require.Equal(t, 0, n, "fixture precondition: the table starts empty")

	rec := h.client(t).do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "admin@example.com", "name": "Ana", "password": "correct horse battery",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	user := decode(t, rec)["user"].(map[string]any)
	// Owner, not admin: whoever completes setup holds the instance (ADR-33).
	assert.Equal(t, "owner", user["role"])
	assert.Equal(t, "active", user["status"])
}

func TestBootstrap_RefusedOnceAnAccountIsActive(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "correct horse battery")

	rec := h.client(t).do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "second@example.com", "name": "Bob", "password": "another password!!",
	})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "already_configured", errCode(t, rec))
}

func TestBootstrap_RejectsWeakPasswordAndBadEmail(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)

	rec := c.do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "admin@example.com", "name": "A", "password": "short",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "password_too_short", errCode(t, rec))

	rec = c.do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "not-an-email", "name": "A", "password": "long enough password",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_email", errCode(t, rec))
}

// bcrypt silently truncates at 72 bytes. Accepting a longer passphrase and
// honouring only its prefix is a lie the user never discovers — until the day
// they type the first 72 characters and get in.
func TestBootstrap_RejectsPasswordBeyondBcryptLimit(t *testing.T) {
	h := newHarness(t)
	rec := h.client(t).do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "admin@example.com", "name": "A", "password": strings.Repeat("a", 73),
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "password_too_long", errCode(t, rec))
}

// ─────────────────────────────────────────────────────────────────────
// Login
// ─────────────────────────────────────────────────────────────────────

func TestLogin_SucceedsAndSetsTheCookieTriple(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "authenticated", decode(t, rec)["status"])

	at := cookieByName(rec, auth.CookieAccess)
	require.NotNil(t, at, "login must set the access cookie")
	assert.True(t, at.HttpOnly, "the access token must be unreadable from JavaScript")
	assert.True(t, at.Secure)
	assert.Equal(t, http.SameSiteLaxMode, at.SameSite)
	assert.Equal(t, "/", at.Path)

	rt := cookieByName(rec, auth.CookieRefresh)
	require.NotNil(t, rt)
	assert.True(t, rt.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, rt.SameSite,
		"the refresh cookie is never needed on a cross-site navigation")
	assert.Equal(t, "/api/auth", rt.Path,
		"scoping the refresh cookie keeps it off every content request")

	csrf := cookieByName(rec, auth.CookieCSRF)
	require.NotNil(t, csrf)
	assert.False(t, csrf.HttpOnly,
		"the SPA has to read fx_csrf to echo it; its protection is that a "+
			"cross-origin attacker cannot READ it, not that our own script cannot")
}

// The anti-enumeration contract: three different underlying failures must be
// indistinguishable on the wire. A distinct code for a disabled account would
// confirm the address is registered.
func TestLogin_FailuresAreByteIdentical(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "real@example.com", "a good password", "editor")
	disabled := testdb.SeedUserWithPassword(t, h.pool, "disabled@example.com", "a good password", "editor")
	testdb.SetUserStatus(t, h.pool, disabled, "disabled")

	cases := []struct {
		name  string
		email string
		pass  string
	}{
		{"unknown e-mail", "ghost@example.com", "a good password"},
		{"wrong password", "real@example.com", "wrong password here"},
		{"disabled account", "disabled@example.com", "a good password"},
	}

	var bodies []string
	for _, tc := range cases {
		// Each case gets a fresh router so the shared rate-limit budget cannot
		// turn the third attempt into a 429 and mask the comparison.
		hh := newHarnessOn(t, h.pool)
		rec := hh.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": tc.email, "password": tc.pass,
		})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s: %s", tc.name, rec.Body.String())
		assert.Nil(t, cookieByName(rec, auth.CookieAccess), "%s must not set a session", tc.name)
		bodies = append(bodies, rec.Body.String())
	}

	assert.Equal(t, bodies[0], bodies[1], "unknown e-mail and wrong password must be identical")
	assert.Equal(t, bodies[0], bodies[2], "a disabled account must not be distinguishable")
	assert.Contains(t, bodies[0], "invalid_credentials")
}

// The timing half of the same contract. Skipping bcrypt on an unknown e-mail
// makes the miss return in microseconds, and that gap alone is a working
// account-enumeration oracle.
func TestLogin_UnknownEmailCostsTheSameAsAWrongPassword(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "real@example.com", "a good password", "editor")

	measure := func(email string) time.Duration {
		hh := newHarnessOn(t, h.pool)
		start := time.Now()
		hh.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": email, "password": "definitely wrong",
		})
		return time.Since(start)
	}

	miss := measure("ghost@example.com")
	hit := measure("real@example.com")

	// Both must clear the 250 ms floor. The floor is what actually erases the
	// signal; asserting on it (rather than on the ratio between the two) keeps
	// the test stable on a loaded CI box.
	assert.GreaterOrEqual(t, miss, 240*time.Millisecond,
		"an unknown e-mail returned in %v — the duration floor is not being applied", miss)
	assert.GreaterOrEqual(t, hit, 240*time.Millisecond)
}

func TestLogin_PendingAccountCannotSignIn(t *testing.T) {
	h := newHarness(t)
	uid := testdb.SeedUserWithPassword(t, h.pool, "pending@example.com", "a good password", "editor")
	testdb.SetUserStatus(t, h.pool, uid, "pending")

	rec := h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "pending@example.com", "password": "a good password",
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", errCode(t, rec))
}

// An account with no password (an unclaimed invite) must not be loggable into
// with an empty string. bcrypt against a NULL hash has to fail closed.
func TestLogin_AccountWithoutPasswordIsNotLoggableInto(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUser(t, h.pool, "nopass@example.com", "editor")

	for _, pw := range []string{"", "anything at all"} {
		rec := h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "nopass@example.com", "password": pw,
		})
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "password %q", pw)
	}
}

func TestLogin_EmailIsCaseAndWhitespaceInsensitive(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	rec := h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "  User@Example.COM  ", "password": "a good password",
	})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestLogin_RateLimitedPerEmail(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "target@example.com", "a good password", "editor")
	c := h.client(t)

	// The e-mail bucket caps at 5 consecutive failures.
	for i := range 5 {
		rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "target@example.com", "password": "wrong",
		})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i+1)
	}
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "target@example.com", "password": "wrong",
	})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "too_many_attempts", errCode(t, rec))
	assert.NotEmpty(t, rec.Header().Get("Retry-After"), "a 429 must tell the client when to retry")

	// The correct password is refused too: a lockout that a valid credential
	// walks straight through protects nothing.
	rec = c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "target@example.com", "password": "a good password",
	})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}

// Not incrementing the bucket for an unknown address is itself an oracle: the
// attacker learns which addresses are lockable, and therefore which exist.
func TestLogin_RateLimitCountsUnknownEmailsToo(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	for range 5 {
		c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "ghost@example.com", "password": "wrong",
		})
	}
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "ghost@example.com", "password": "wrong",
	})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"an unknown address must consume its bucket exactly like a real one")
}

// ─────────────────────────────────────────────────────────────────────
// /me
// ─────────────────────────────────────────────────────────────────────

// /me answering 200 for an anonymous caller is a contract, not an accident: a
// 401 would recurse through the SPA's refresh interceptor on every cold boot.
func TestMe_IsAlways200(t *testing.T) {
	h := newHarness(t)

	rec := h.client(t).do(http.MethodGet, "/api/auth/me", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decode(t, rec)
	assert.Equal(t, "setup_required", body["status"],
		"before bootstrap, /me must say so rather than merely 'anonymous'")

	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec = h.client(t).do(http.MethodGet, "/api/auth/me", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "anonymous", decode(t, rec)["status"])
}

func TestMe_ReportsTheAuthenticatedUser(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := c.do(http.MethodGet, "/api/auth/me", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decode(t, rec)
	assert.Equal(t, "authenticated", body["status"])
	assert.Equal(t, "admin@example.com", body["user"].(map[string]any)["email"])
	assert.NotEmpty(t, body["csrf_token"])
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"),
		"/api/auth responses carry session state and must never be cached")
}

// The password hash must never reach the wire, under any key.
func TestMe_NeverLeaksTheHash(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := c.do(http.MethodGet, "/api/auth/me", nil)
	assert.NotContains(t, rec.Body.String(), "$2a$", "bcrypt hash leaked into /me")
	assert.NotContains(t, rec.Body.String(), "password_hash")
	// has_password is the safe projection the UI actually needs.
	assert.Equal(t, true, decode(t, rec)["user"].(map[string]any)["has_password"])
}

// ─────────────────────────────────────────────────────────────────────
// Session middleware, CSRF, RBAC
// ─────────────────────────────────────────────────────────────────────

func TestAuthenticate_RejectsMissingAndForgedCookies(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := h.client(t).do(http.MethodGet, "/api/links", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "no cookie must not reach content")

	forged := h.client(t)
	forged.cookies[auth.CookieAccess] = "obviously-not-a-real-token"
	rec = forged.do(http.MethodGet, "/api/links", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotNil(t, cookieByName(rec, auth.CookieAccess),
		"a dead cookie must be cleared so the browser stops replaying it")
}

func TestAuthenticate_PassesThePrincipalThrough(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := c.do(http.MethodGet, "/api/links", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, float64(1), decode(t, rec)["uid"],
		"the handler must see the bootstrap admin's id, not a zero owner")
}

func TestCSRF_UnsafeVerbsRequireTheHeader(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	// Safe verbs never need it.
	rec := c.doRaw(http.MethodGet, "/api/links", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Unsafe verb, no header: refused.
	rec = c.doRaw(http.MethodPost, "/api/links", map[string]string{}, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "csrf_failed", errCode(t, rec))

	// Unsafe verb, wrong header: refused.
	rec = c.doRaw(http.MethodPost, "/api/links", map[string]string{},
		map[string]string{auth.CSRFHeader: "not-the-token"})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Correct header: allowed.
	rec = c.do(http.MethodPost, "/api/links", map[string]string{})
	assert.Equal(t, http.StatusCreated, rec.Code)
}

// The reason the check is against the SESSION ROW and not against the cookie.
//
// Naive double-submit (header == cookie) is defeated by cookie injection from a
// sibling subdomain: the attacker sets fx_csrf themselves and echoes the same
// value, controlling both sides of the comparison. Here the attacker's matched
// pair is still refused, because the server compares against what IT stored.
func TestCSRF_MatchingHeaderAndCookieIsNotEnough(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	injected := "attacker-chosen-value"
	c.cookies[auth.CookieCSRF] = injected

	rec := c.doRaw(http.MethodPost, "/api/links", map[string]string{},
		map[string]string{auth.CSRFHeader: injected})

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"a self-consistent header/cookie pair must still fail against the stored hash")
}

func TestRequireAdmin_HidesTheSurfaceFromNonAdmins(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)

	rec := c.do(http.MethodGet, "/api/admin/users", nil)
	// 404, not 403: a 403 confirms the route exists and that the caller merely
	// lacks the role, telling an attacker exactly what to escalate toward.
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, rec.Body.String(), "admin@example.com")
}

// ─────────────────────────────────────────────────────────────────────
// Refresh rotation
// ─────────────────────────────────────────────────────────────────────

func TestRefresh_RotatesEveryToken(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	oldAT := c.cookies[auth.CookieAccess]
	oldRT := c.cookies[auth.CookieRefresh]
	oldCSRF := c.cookies[auth.CookieCSRF]

	rec := c.do(http.MethodPost, "/api/auth/refresh", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.NotEqual(t, oldAT, c.cookies[auth.CookieAccess])
	assert.NotEqual(t, oldRT, c.cookies[auth.CookieRefresh])
	assert.NotEqual(t, oldCSRF, c.cookies[auth.CookieCSRF],
		"the CSRF token rotates with the session, or a leaked one outlives it")

	var accessHash, refreshHash, csrfHash []byte
	var accessExpires, refreshExpires time.Time
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT access_token_hash, refresh_token_hash, csrf_token_hash,
		       access_expires_at, refresh_expires_at
		FROM session WHERE refresh_token_hash = $1`,
		secrets.Hash(c.cookies[auth.CookieRefresh])).Scan(
		&accessHash, &refreshHash, &csrfHash, &accessExpires, &refreshExpires))
	assert.Equal(t, secrets.Hash(c.cookies[auth.CookieAccess]), accessHash)
	assert.Equal(t, secrets.Hash(c.cookies[auth.CookieRefresh]), refreshHash)
	assert.Equal(t, secrets.Hash(c.cookies[auth.CookieCSRF]), csrfHash)
	assert.WithinDuration(t, time.Now().Add(testSessionTTL().Access), accessExpires, time.Second)
	assert.WithinDuration(t, time.Now().Add(testSessionTTL().Refresh), refreshExpires, time.Second)

	// The old access token must be dead immediately, not merely superseded.
	stale := h.client(t)
	stale.cookies[auth.CookieAccess] = oldAT
	assert.Equal(t, http.StatusUnauthorized, stale.do(http.MethodGet, "/api/links", nil).Code)

	// The new one works.
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/links", nil).Code)
}

// The grace window is what separates a correct rotation scheme from an unusable
// one. React StrictMode double-mounts, two tabs, or a fast reload all fire two
// /refresh calls with the SAME cookie; without the window the second is
// classified as replay and the user is signed out at random.
func TestRefresh_GraceWindowToleratesARacingTab(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	firstRT := c.cookies[auth.CookieRefresh]

	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	// A second tab still holding the original cookie.
	tab2 := h.client(t)
	tab2.cookies[auth.CookieRefresh] = firstRT
	rec := tab2.do(http.MethodPost, "/api/auth/refresh", nil)

	require.Equal(t, http.StatusOK, rec.Code,
		"a re-presented token inside the grace window is a racing tab, not an attack")
	assert.Equal(t, http.StatusOK, tab2.do(http.MethodGet, "/api/links", nil).Code)
	// The first tab must survive too. Both requests share one cookie jar, so if
	// the grace path overwrote the winner's row the browser would keep whichever
	// response landed last while the server held the other — a coin-flip logout.
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/links", nil).Code,
		"the grace path must not invalidate the tokens the winning request installed")
}

func TestRefresh_ExactlyConcurrentRequestsBothReachGraceSemantics(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	refresh := c.cookies[auth.CookieRefresh]
	ctx := context.Background()

	// Hold the session row so both serializable transactions establish their
	// pre-rotation snapshot before either can claim the token.
	blocker, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var sessionID int64
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT id FROM session WHERE refresh_token_hash = $1 FOR UPDATE`, secrets.Hash(refresh)).Scan(&sessionID))

	tabs := []*client{h.client(t), h.client(t)}
	results := make(chan *httptest.ResponseRecorder, len(tabs))
	for _, tab := range tabs {
		tab.cookies[auth.CookieRefresh] = refresh
		go func(tab *client) {
			results <- tab.do(http.MethodPost, "/api/auth/refresh", nil)
		}(tab)
	}
	waitForBlockedSQLCount(t, h.pool, "FOR UPDATE OF s", len(tabs))
	require.NoError(t, blocker.Commit(ctx))

	for range tabs {
		rec := <-results
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	for _, tab := range tabs {
		assert.Equal(t, http.StatusOK, tab.do(http.MethodGet, "/api/links", nil).Code)
	}
	var sessions, families int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT family_id) FROM session WHERE revoked_at IS NULL`).Scan(
		&sessions, &families))
	assert.Equal(t, 2, sessions)
	assert.Equal(t, 1, families)
}

// The grace path issues a SIBLING session, which must stay inside the same
// family and inherit its birth date.
//
// Family: reuse detection revokes by family_id, so a sibling in a family of its
// own would survive a replay that killed everything else. created_at: the
// 90-day absolute ceiling is measured from it, so a sibling stamped `now()`
// would let a client ride the grace window on every rotation and push the
// ceiling forward forever — exactly the immortality it exists to prevent.
func TestRefresh_GraceSiblingInheritsFamilyAndAbsoluteCeiling(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	firstRT := c.cookies[auth.CookieRefresh]

	ctx := context.Background()
	// Age the original session so an inherited created_at is unmistakable.
	_, err := h.pool.Exec(ctx, `UPDATE session SET created_at = now() - interval '40 days'`)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	tab2 := h.client(t)
	tab2.cookies[auth.CookieRefresh] = firstRT
	require.Equal(t, http.StatusOK, tab2.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	var families, sessions int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT count(DISTINCT family_id), count(*) FROM session`).Scan(&families, &sessions))
	assert.Equal(t, 2, sessions, "the grace path adds one sibling session")
	assert.Equal(t, 1, families, "the sibling must stay in the original family")

	var ageDays float64
	var accessHash, refreshHash, csrfHash []byte
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT EXTRACT(epoch FROM now() - created_at) / 86400,
		        access_token_hash, refresh_token_hash, csrf_token_hash
		 FROM session ORDER BY id DESC LIMIT 1`).Scan(
		&ageDays, &accessHash, &refreshHash, &csrfHash))
	assert.InDelta(t, 40, ageDays, 1,
		"the sibling must inherit the family's birth date, not reset the absolute ceiling")
	assert.Equal(t, secrets.Hash(tab2.cookies[auth.CookieAccess]), accessHash)
	assert.Equal(t, secrets.Hash(tab2.cookies[auth.CookieRefresh]), refreshHash)
	assert.Equal(t, secrets.Hash(tab2.cookies[auth.CookieCSRF]), csrfHash)
}

// The sibling must die with its family. If it escaped family revocation, a
// thief who timed a replay inside the grace window would keep a live session
// after the detector fired.
func TestRefresh_GraceSiblingDiesWithTheFamilyOnReuse(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	firstRT := c.cookies[auth.CookieRefresh]
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	sibling := h.client(t)
	sibling.cookies[auth.CookieRefresh] = firstRT
	require.Equal(t, http.StatusOK, sibling.do(http.MethodPost, "/api/auth/refresh", nil).Code)
	require.Equal(t, http.StatusOK, sibling.do(http.MethodGet, "/api/links", nil).Code)

	// Now trigger genuine reuse detection with an aged consumed token.
	stolen := c.cookies[auth.CookieRefresh]
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)
	_, err := h.pool.Exec(context.Background(),
		`UPDATE session_used_token SET used_at = now() - interval '1 hour'`)
	require.NoError(t, err)

	thief := h.client(t)
	thief.cookies[auth.CookieRefresh] = stolen
	require.Equal(t, http.StatusUnauthorized, thief.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	assert.Equal(t, http.StatusUnauthorized, sibling.do(http.MethodGet, "/api/links", nil).Code,
		"a grace-spawned sibling must be revoked with the rest of its family")
}

// Outside the window the same input is an attack, and the response is the whole
// family — not just the presented token, whose successor the thief already
// holds.
func TestRefresh_ReplayOutsideGraceRevokesTheWholeFamily(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	stolenRT := c.cookies[auth.CookieRefresh]

	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	// Age the consumed-token record past the 10 s grace window.
	_, err := h.pool.Exec(context.Background(),
		`UPDATE session_used_token SET used_at = now() - interval '1 hour'`)
	require.NoError(t, err)

	thief := h.client(t)
	thief.cookies[auth.CookieRefresh] = stolenRT
	rec := thief.do(http.MethodPost, "/api/auth/refresh", nil)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "session_revoked", errCode(t, rec))

	// The legitimate session — whose tokens the thief never held — is dead too.
	// Revoking only the replayed token would leave the attacker's rotated one
	// alive, which is the entire point of family revocation.
	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, "/api/links", nil).Code,
		"the victim's live session must die with the family")
	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	var reason string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT revoked_reason FROM session ORDER BY id DESC LIMIT 1`).Scan(&reason))
	assert.Equal(t, "reuse_detected", reason)
	var usedTokens int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session_used_token`).Scan(&usedTokens))
	assert.Zero(t, usedTokens, "replay teardown must purge the family's consumed-token trail")
}

func TestRefresh_WarnsTheOwnerAboutReuse(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	stolen := c.cookies[auth.CookieRefresh]
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)
	_, err := h.pool.Exec(context.Background(),
		`UPDATE session_used_token SET used_at = now() - interval '1 hour'`)
	require.NoError(t, err)

	thief := h.client(t)
	thief.cookies[auth.CookieRefresh] = stolen
	thief.do(http.MethodPost, "/api/auth/refresh", nil)

	// The notification is fire-and-forget on a detached context so the response
	// never waits on SMTP; poll briefly rather than sleeping a fixed amount.
	require.Eventually(t, func() bool { return len(h.mail.all()) > 0 },
		3*time.Second, 25*time.Millisecond, "the owner must be told their sessions were killed")
	assert.Equal(t, "admin@example.com", h.mail.all()[0].To)
	assert.NotContains(t, h.mail.all()[0].Text, "http",
		"the warning must carry no link — that shape is indistinguishable from phishing")
}

func TestRefresh_WithoutACookieIs401(t *testing.T) {
	h := newHarness(t)
	rec := h.client(t).do(http.MethodPost, "/api/auth/refresh", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "no_session", errCode(t, rec))
}

func TestRefresh_RefusedForADisabledAccount(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)

	testdb.SetUserStatus(t, h.pool, uid, "disabled")

	rec := c.do(http.MethodPost, "/api/auth/refresh", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a disabled user must not be able to refresh their way back in")
	assert.Equal(t, "account_inactive", errCode(t, rec))
}

// A disabled account's IN-FLIGHT access token must stop working immediately,
// not merely at its next expiry. That is what the join onto app_user in
// ResolveAccess buys.
func TestAuthenticate_DisabledMidSessionIsRejectedAtOnce(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)
	require.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/links", nil).Code)

	testdb.SetUserStatus(t, h.pool, uid, "disabled")

	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, "/api/links", nil).Code)
}

// ─────────────────────────────────────────────────────────────────────
// Logout / sessions
// ─────────────────────────────────────────────────────────────────────

func TestLogout_KillsTheSessionAndClearsEveryCookie(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	at := c.cookies[auth.CookieAccess]
	rt := c.cookies[auth.CookieRefresh]

	rec := c.do(http.MethodPost, "/api/auth/logout", nil)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// Every cookie must be cleared at the SAME path it was set with. Clearing
	// fx_rt at "/" would leave the real cookie at /api/auth alive — a logout
	// that leaves a working refresh token behind.
	cleared := map[string]string{}
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 {
			cleared[ck.Name] = ck.Path
		}
	}
	assert.Equal(t, "/", cleared[auth.CookieAccess])
	assert.Equal(t, "/", cleared[auth.CookieCSRF])
	assert.Equal(t, "/api/auth", cleared[auth.CookieRefresh])

	// And the tokens must be dead server-side, not merely dropped by the client.
	zombie := h.client(t)
	zombie.cookies[auth.CookieAccess] = at
	assert.Equal(t, http.StatusUnauthorized, zombie.do(http.MethodGet, "/api/links", nil).Code)
	zombie.cookies = map[string]string{auth.CookieRefresh: rt}
	assert.Equal(t, http.StatusUnauthorized, zombie.do(http.MethodPost, "/api/auth/refresh", nil).Code)
}

func TestLogout_StaleCookiesRacingSuccessfulRefreshRevokeTheLiveFamily(t *testing.T) {
	h := newHarness(t)
	refreshed := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	stale := clientOnHarness(t, h, refreshed)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	for name, value := range refreshed.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	gate := &delayedResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
		reached:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		h.router.ServeHTTP(gate, req)
		close(done)
	}()

	select {
	case <-gate.reached:
		// Rotate has committed and set the replacement cookies, but its response
		// has not reached the browser yet. Logout therefore carries the old jar.
	case <-time.After(3 * time.Second):
		close(gate.release)
		t.Fatal("refresh did not reach its delayed response")
	}

	logout := stale.do(http.MethodPost, "/api/auth/logout", nil)
	close(gate.release)
	<-done
	require.Equal(t, http.StatusNoContent, logout.Code)
	require.Equal(t, http.StatusOK, gate.Code, gate.Body.String())
	refreshed.absorb(gate.ResponseRecorder)

	assert.Equal(t, http.StatusUnauthorized, refreshed.do(http.MethodGet, "/api/links", nil).Code,
		"the delayed refresh response must not restore a live access token after logout")
	assert.Equal(t, http.StatusUnauthorized, refreshed.do(http.MethodPost, "/api/auth/refresh", nil).Code,
		"logout must revoke the family resolved through the consumed refresh token")

	var live int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session WHERE revoked_at IS NULL`).Scan(&live))
	assert.Zero(t, live)
}

// Logout is idempotent: the common case is a user clicking it on a stale tab,
// and the only wrong outcome is one where their cookies survive.
func TestLogout_IsIdempotent(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	assert.Equal(t, http.StatusNoContent, c.do(http.MethodPost, "/api/auth/logout", nil).Code)
	assert.Equal(t, http.StatusNoContent, c.do(http.MethodPost, "/api/auth/logout", nil).Code)
	assert.Equal(t, http.StatusNoContent, h.client(t).do(http.MethodPost, "/api/auth/logout", nil).Code)
}

func TestSessions_ListAndRevoke(t *testing.T) {
	h := newHarness(t)
	c1 := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	c2 := h.client(t)
	require.Equal(t, http.StatusOK, c2.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	rec := c1.do(http.MethodGet, "/api/auth/sessions", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	sessions := decode(t, rec)["sessions"].([]any)
	require.Len(t, sessions, 2)

	current := 0
	var other float64
	for _, s := range sessions {
		m := s.(map[string]any)
		if m["current"].(bool) {
			current++
		} else {
			other = m["id"].(float64)
		}
	}
	assert.Equal(t, 1, current, "exactly one session is the caller's own")

	rec = c1.do(http.MethodDelete, fmt.Sprintf("/api/auth/sessions/%d", int64(other)), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, http.StatusUnauthorized, c2.do(http.MethodGet, "/api/links", nil).Code)
	assert.Equal(t, http.StatusOK, c1.do(http.MethodGet, "/api/links", nil).Code)
}

// The user_id predicate on RevokeSession is what makes the id in the URL
// harmless: without it, any signed-in user could sign out any other by guessing
// a dense BIGSERIAL.
func TestSessions_CannotRevokeAnotherUsersSession(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "victim@example.com", "a good password", "editor")

	victim := h.client(t)
	require.Equal(t, http.StatusOK, victim.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "victim@example.com", "password": "a good password",
	}).Code)

	var victimSession int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT s.id FROM session s JOIN app_user u ON u.id = s.user_id
		 WHERE u.email_normalized = 'victim@example.com'`).Scan(&victimSession))

	rec := admin.do(http.MethodDelete, fmt.Sprintf("/api/auth/sessions/%d", victimSession), nil)
	assert.Equal(t, http.StatusNotFound, rec.Code,
		"another user's session id must be indistinguishable from a non-existent one")
	assert.Equal(t, http.StatusOK, victim.do(http.MethodGet, "/api/links", nil).Code,
		"the victim's session must be untouched")
}

func TestLogoutAll_RevokesEverySession(t *testing.T) {
	h := newHarness(t)
	c1 := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	c2 := h.client(t)
	require.Equal(t, http.StatusOK, c2.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	require.Equal(t, http.StatusNoContent, c1.do(http.MethodPost, "/api/auth/logout-all", nil).Code)

	assert.Equal(t, http.StatusUnauthorized, c2.do(http.MethodGet, "/api/links", nil).Code)
}

// ─────────────────────────────────────────────────────────────────────
// Password change
// ─────────────────────────────────────────────────────────────────────

func TestChangePassword_RequiresTheCurrentOne(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := c.do(http.MethodPost, "/api/auth/password/change", map[string]string{
		"current_password": "not the right one", "new_password": "a brand new password",
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"without this, a stolen session upgrades into permanent account takeover")
	assert.Equal(t, "invalid_credentials", errCode(t, rec))
}

func TestChangePassword_KeepsThisSessionAndKillsTheRest(t *testing.T) {
	h := newHarness(t)
	c1 := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	c2 := h.client(t)
	require.Equal(t, http.StatusOK, c2.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)

	rec := c1.do(http.MethodPost, "/api/auth/password/change", map[string]string{
		"current_password": "a good password", "new_password": "a brand new password",
	})
	require.Equal(t, http.StatusNoContent, rec.Code)

	assert.Equal(t, http.StatusOK, c1.do(http.MethodGet, "/api/links", nil).Code,
		"signing the user out of the device they are using would be hostile")
	assert.Equal(t, http.StatusUnauthorized, c2.do(http.MethodGet, "/api/links", nil).Code,
		"a password change is how a user reacts to compromise — other sessions must die")

	fresh := h.client(t)
	assert.Equal(t, http.StatusUnauthorized, fresh.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a good password",
	}).Code)
	assert.Equal(t, http.StatusOK, fresh.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin@example.com", "password": "a brand new password",
	}).Code)
}

func TestChangePassword_ConcurrentOldProofHasExactlyOneWinner(t *testing.T) {
	h := newHarness(t)
	first := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	second := clientOnHarness(t, h, first)

	type result struct {
		status int
		code   string
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, c := range []*client{first, second} {
		wg.Add(1)
		go func(i int, c *client) {
			defer wg.Done()
			<-start
			rec := c.do(http.MethodPost, "/api/auth/password/change", map[string]string{
				"current_password": "a good password",
				"new_password":     fmt.Sprintf("a concurrent password %d", i+1),
			})
			out := result{status: rec.Code}
			if rec.Code != http.StatusNoContent {
				out.code = errCode(t, rec)
			}
			results <- out
		}(i, c)
	}
	close(start)
	wg.Wait()
	close(results)

	successes, rejected := 0, 0
	for got := range results {
		switch got.status {
		case http.StatusNoContent:
			successes++
		case http.StatusUnauthorized:
			rejected++
			assert.Equal(t, "invalid_credentials", got.code)
		default:
			t.Fatalf("unexpected change-password result: status=%d code=%q", got.status, got.code)
		}
	}
	assert.Equal(t, 1, successes, "both requests changed the password from one old proof")
	assert.Equal(t, 1, rejected)

	workingPasswords := 0
	for i := range 2 {
		if h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "admin@example.com", "password": fmt.Sprintf("a concurrent password %d", i+1),
		}).Code == http.StatusOK {
			workingPasswords++
		}
	}
	assert.Equal(t, 1, workingPasswords)
}

func TestChangePassword_EnforcesThePolicy(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := c.do(http.MethodPost, "/api/auth/password/change", map[string]string{
		"current_password": "a good password", "new_password": "short",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "password_too_short", errCode(t, rec))
}

// ─────────────────────────────────────────────────────────────────────
// Invites
// ─────────────────────────────────────────────────────────────────────

func inviteToken(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	acceptURL, _ := decode(t, rec)["accept_url"].(string)
	parsed, err := url.Parse(acceptURL)
	require.NoError(t, err)
	require.Empty(t, parsed.RawQuery, "the initial HTTP request must not carry the invite token")
	_, tok, ok := strings.Cut(parsed.Fragment, "invite=")
	require.True(t, ok, "accept_url %q must carry the raw token in its fragment", acceptURL)
	return tok
}

func TestInvite_FullRoundTrip(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := admin.do(http.MethodPost, "/api/admin/invites", map[string]string{
		"email": "newcomer@example.com", "role": "editor",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	tok := inviteToken(t, rec)

	// The link must have been mailed too, not only returned.
	msg := h.mail.waitFor(t, "newcomer@example.com")
	assert.Contains(t, msg.Text, tok)

	// The accept screen resolves the token to show which address it binds.
	newcomer := h.client(t)
	rec = newcomer.do(http.MethodPost, "/api/auth/invites/lookup", map[string]string{"token": tok})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "newcomer@example.com", decode(t, rec)["email"])

	rec = newcomer.do(http.MethodPost, "/api/auth/invites/accept", map[string]string{
		"token": tok, "name": "New Comer", "password": "a fresh new password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Equal(t, "authenticated", body["status"], "accepting an invite signs you straight in")
	user := body["user"].(map[string]any)
	assert.Equal(t, "newcomer@example.com", user["email"])
	assert.Equal(t, "editor", user["role"])

	assert.Equal(t, http.StatusOK, newcomer.do(http.MethodGet, "/api/links", nil).Code)
}

// Single use. Without the FOR UPDATE inside the accepting transaction, two
// requests racing on the same token would each create an account.
func TestInvite_TokenIsSingleUse(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	rec := admin.do(http.MethodPost, "/api/admin/invites", map[string]string{
		"email": "once@example.com", "role": "editor",
	})
	tok := inviteToken(t, rec)

	require.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/invites/accept",
		map[string]string{"token": tok, "name": "A", "password": "a fresh new password"}).Code)

	rec = h.client(t).do(http.MethodPost, "/api/auth/invites/accept",
		map[string]string{"token": tok, "name": "B", "password": "another password!!"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "invite_invalid", errCode(t, rec))

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user WHERE email_normalized = 'once@example.com'`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestInvite_ExpiredAndRevokedTokensAreIndistinguishableFromUnknown(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "expired@example.com", "role": "editor"})
	expiredTok := inviteToken(t, rec)
	_, err := h.pool.Exec(context.Background(),
		`UPDATE invite SET expires_at = now() - interval '1 day'`)
	require.NoError(t, err)

	rec = admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "revoked@example.com", "role": "editor"})
	revokedTok := inviteToken(t, rec)
	var revokedID int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT id FROM invite WHERE email_normalized = 'revoked@example.com'`).Scan(&revokedID))
	require.Equal(t, http.StatusNoContent,
		admin.do(http.MethodDelete, fmt.Sprintf("/api/admin/invites/%d", revokedID), nil).Code)

	var bodies []string
	for _, tok := range []string{expiredTok, revokedTok, "a-token-that-never-existed"} {
		rec := h.client(t).do(http.MethodPost, "/api/auth/invites/lookup", map[string]string{"token": tok})
		require.Equal(t, http.StatusNotFound, rec.Code)
		bodies = append(bodies, rec.Body.String())
	}
	assert.Equal(t, bodies[0], bodies[1])
	assert.Equal(t, bodies[0], bodies[2],
		"expired, revoked and unknown must be one indistinguishable failure")
}

// Re-inviting the same address replaces the open invite instead of failing —
// an admin clicking "invite" twice means "send it again". The partial unique
// index would otherwise reject the second.
func TestInvite_ReinvitingSupersedesTheOpenOne(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	first := inviteToken(t, admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "twice@example.com", "role": "editor"}))
	rec := admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "twice@example.com", "role": "editor"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	second := inviteToken(t, rec)

	require.NotEqual(t, first, second)
	assert.Equal(t, http.StatusNotFound,
		h.client(t).do(http.MethodPost, "/api/auth/invites/lookup", map[string]string{"token": first}).Code,
		"the superseded token must stop working")
	assert.Equal(t, http.StatusOK,
		h.client(t).do(http.MethodPost, "/api/auth/invites/lookup", map[string]string{"token": second}).Code)
}

func TestInvite_RefusedForAnExistingAccount(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "taken@example.com", "a good password", "editor")

	rec := admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "taken@example.com", "role": "editor"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "email_taken", errCode(t, rec))
}

// The role comes from the INVITE row, never from the acceptance payload —
// otherwise anyone with a user invite could mint themselves an admin account.
func TestInvite_AcceptCannotEscalateItsOwnRole(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	tok := inviteToken(t, admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "sneaky@example.com", "role": "editor"}))

	rec := h.client(t).doRaw(http.MethodPost, "/api/auth/invites/accept", map[string]any{
		"token": tok, "name": "S", "password": "a fresh new password", "role": "admin",
	}, nil)
	// DisallowUnknownFields means the extra key is rejected outright, which is
	// the strongest possible answer.
	require.Equal(t, http.StatusBadRequest, rec.Code)

	require.Equal(t, http.StatusOK, h.client(t).do(http.MethodPost, "/api/auth/invites/accept",
		map[string]string{"token": tok, "name": "S", "password": "a fresh new password"}).Code)

	var role string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT role FROM app_user WHERE email_normalized = 'sneaky@example.com'`).Scan(&role))
	assert.Equal(t, "editor", role)
}

// ─────────────────────────────────────────────────────────────────────
// Admin users
// ─────────────────────────────────────────────────────────────────────

func TestAdmin_ListUsersNeverIncludesHashes(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	rec := admin.do(http.MethodGet, "/api/admin/users", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, decode(t, rec)["users"], 2)
	assert.NotContains(t, rec.Body.String(), "$2a$")
	assert.NotContains(t, rec.Body.String(), "password_hash")
}

func TestAdmin_DisableRevokesTheUsersSessionsImmediately(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	victim := h.client(t)
	require.Equal(t, http.StatusOK, victim.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)

	rec := admin.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(uid)),
		map[string]string{"status": "disabled"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, http.StatusUnauthorized, victim.do(http.MethodGet, "/api/links", nil).Code,
		"a ban that only takes effect at the next token expiry is not a ban")
}

func TestAdmin_CannotDemoteOrDisableSelf(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	for _, patch := range []map[string]string{{"role": "editor"}, {"status": "disabled"}} {
		rec := admin.do(http.MethodPatch, "/api/admin/users/1", patch)
		assert.Equal(t, http.StatusConflict, rec.Code, "patch %v", patch)
		assert.Equal(t, "self_target", errCode(t, rec))
	}
	rec := admin.do(http.MethodDelete, "/api/admin/users/1", nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// Zero active administrators is an unrecoverable state — no API call can undo
// it, only a direct database edit.
func TestAdmin_CannotRemoveTheLastAdmin(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	second := testdb.SeedUserWithPassword(t, h.pool, "admin2@example.com", "a good password", "admin")

	// With two admins, demoting the other one is allowed.
	rec := admin.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(second)),
		map[string]string{"role": "editor"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Now the caller is the last admin, and cannot be removed by anyone —
	// including via a second admin account that no longer exists.
	promoted := testdb.SeedUserWithPassword(t, h.pool, "admin3@example.com", "a good password", "admin")
	other := h.client(t)
	require.Equal(t, http.StatusOK, other.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin3@example.com", "password": "a good password",
	}).Code)
	// The bootstrap account is the OWNER, and no API call can demote it — that
	// is a separate invariant with its own test. Clearing it in SQL is what
	// leaves admin3 as the genuinely last administrator, which is the state
	// THIS test exists to exercise; going through the API would only re-assert
	// owner immutability.
	_, err := h.pool.Exec(context.Background(),
		`UPDATE app_user SET role = 'editor' WHERE role = 'owner'`)
	require.NoError(t, err)

	// admin3 is now the only active admin: it cannot demote itself, and no one
	// else can either.
	rec = other.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(promoted)),
		map[string]string{"role": "editor"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "self_target", errCode(t, rec))
}

// The test above never reaches the `last_admin` answer, and cannot: the role is
// read live from app_user on every request, so a caller who is an active admin
// is always counted in the guard's own tally. No SEQUENTIAL request can be the
// one that empties the set — the only sequential way to try is to target
// yourself, which the self-target guard refuses first.
//
// Concurrency is the only way in, and it is what the guard exists for: two
// admins demoting each other at the same instant. A read-then-write check would
// let both observe two admins and both proceed, leaving zero — a state no API
// call can undo.
//
// The loser is refused by whichever guard it reaches first, and WHICH one is a
// genuine race: if it clears RequireAdmin before the winner commits it lands in
// guardLastAdminTx and gets 409 `last_admin`; if the commit beats it there, its
// own role is already `user` and RequireAdmin answers 404. Both are correct
// refusals, so this asserts the invariant that holds either way rather than
// pinning a timing. Rounds are repeated because a single pass exercises only
// one side of the race.
func TestAdmin_ConcurrentDemotionsAlwaysLeaveAnAdmin(t *testing.T) {
	for round := range 5 {
		// A fresh harness per round: it resets the schema (restoring the
		// bootstrap placeholder the first client claims) and rebuilds the
		// router, so the round's login does not spend the previous round's
		// rate-limit budget.
		h := newHarness(t)

		first := h.bootstrapAdmin(t, fmt.Sprintf("admin%d@example.com", round), "a good password")
		const firstID = 1 // bootstrap claims the placeholder row
		email2 := fmt.Sprintf("admin2-%d@example.com", round)
		secondID := testdb.SeedUserWithPassword(t, h.pool, email2, "a good password", "admin")

		// Both principals must be ordinary admins for the COUNTING guard to be
		// what decides. Left as owner, `first` would be refused by owner
		// immutability instead — a different invariant, tested separately — and
		// the race this test exists to exercise would never run.
		_, err := h.pool.Exec(context.Background(),
			`UPDATE app_user SET role = 'admin' WHERE role = 'owner'`)
		require.NoError(t, err)

		second := h.client(t)
		require.Equal(t, http.StatusOK, second.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": email2, "password": "a good password",
		}).Code)

		// Each client owns its own cookie map, so the two goroutines share
		// nothing but the router and the pool.
		var recs [2]*httptest.ResponseRecorder
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			recs[0] = first.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(secondID)),
				map[string]string{"role": "editor"})
		}()
		go func() {
			defer wg.Done()
			<-start
			recs[1] = second.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(firstID)),
				map[string]string{"role": "editor"})
		}()
		close(start)
		wg.Wait()

		var admins int
		require.NoError(t, h.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM app_user WHERE role IN ('owner', 'admin') AND status = 'active'`).Scan(&admins))
		require.GreaterOrEqual(t, admins, 1,
			"round %d: both demotions landed (%d/%d) — no administrator left",
			round, recs[0].Code, recs[1].Code)

		refused := 0
		for _, rec := range recs {
			switch rec.Code {
			case http.StatusConflict:
				refused++
				// Not merely "a 409": self_target is also a 409 and would be
				// the wrong refusal here, since neither call targets itself.
				assert.Equal(t, "last_admin", errCode(t, rec))
			case http.StatusNotFound:
				refused++
			}
		}
		assert.Equal(t, 1, refused, "round %d: exactly one demotion must be refused (%d/%d)",
			round, recs[0].Code, recs[1].Code)
	}
}

func TestAdmin_DeleteCascadesTheUsersContent(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "doomed@example.com", "a good password", "editor")

	ctx := context.Background()
	_, err := h.pool.Exec(ctx,
		`INSERT INTO link (user_id, url, title, slug) VALUES ($1, 'https://x.test', 'T', 'doomed-link')`,
		int64(uid))
	require.NoError(t, err)

	rec := admin.do(http.MethodDelete, fmt.Sprintf("/api/admin/users/%d", int64(uid)), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// Cascade is a schema guarantee (ON DELETE CASCADE on user_id), which is
	// why internal/auth never imports links/notes/folders/tags.
	var n int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT count(*) FROM link WHERE user_id = $1`, int64(uid)).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestAdmin_RevokeUserSessions(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	victim := h.client(t)
	require.Equal(t, http.StatusOK, victim.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)

	rec := admin.do(http.MethodPost,
		fmt.Sprintf("/api/admin/users/%d/sessions/revoke", int64(uid)), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, http.StatusUnauthorized, victim.do(http.MethodGet, "/api/links", nil).Code)
}

func TestAdmin_RejectsInvalidRoleAndStatus(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	uid := testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	path := fmt.Sprintf("/api/admin/users/%d", int64(uid))

	rec := admin.do(http.MethodPatch, path, map[string]string{"role": "superuser"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_role", errCode(t, rec))

	// 'pending' is a real CHECK value but must not be settable by an admin:
	// it would strand the account in the bootstrap state.
	rec = admin.do(http.MethodPatch, path, map[string]string{"status": "pending"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_status", errCode(t, rec))
}

func TestAdmin_UnknownUserIs404(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	assert.Equal(t, http.StatusNotFound,
		admin.do(http.MethodPatch, "/api/admin/users/99999", map[string]string{"name": "x"}).Code)
	assert.Equal(t, http.StatusNotFound,
		admin.do(http.MethodDelete, "/api/admin/users/99999", nil).Code)
	assert.Equal(t, http.StatusNotFound,
		admin.do(http.MethodPost, "/api/admin/users/99999/sessions/revoke", nil).Code)
}

// ─────────────────────────────────────────────────────────────────────
// Sweeper
// ─────────────────────────────────────────────────────────────────────

func TestSweep_DropsDeadRowsButKeepsLiveOnes(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	ctx := context.Background()
	// A session long past its refresh expiry, plus a stale consumed token.
	_, err := h.pool.Exec(ctx, `
		INSERT INTO session (user_id, family_id, access_token_hash, access_expires_at,
		                     refresh_token_hash, refresh_expires_at, csrf_token_hash)
		VALUES (1, gen_random_uuid(), '\x01', now() - interval '90 days',
		        '\x02', now() - interval '90 days', '\x03')`)
	require.NoError(t, err)
	_, err = h.pool.Exec(ctx, `UPDATE session_used_token SET used_at = now() - interval '90 days'`)
	require.NoError(t, err)

	n, err := h.repo.Sweep(ctx, 7*24*time.Hour)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(2))

	// The caller's live session survives, and still works.
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/links", nil).Code)
}

// The retention window is the reuse detector's memory. Pruning consumed tokens
// eagerly would turn a replay of a recently-rotated token into an ordinary 401
// instead of the family-killing security event it should be.
func TestSweep_KeepsRecentlyConsumedTokens(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	_, err := h.repo.Sweep(context.Background(), 7*24*time.Hour)
	require.NoError(t, err)

	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session_used_token`).Scan(&n))
	assert.Equal(t, 1, n, "a token consumed seconds ago must survive the sweep")
}

// ─────────────────────────────────────────────────────────────────────
// Sweeper loop
// ─────────────────────────────────────────────────────────────────────

func TestSweeper_RunsOnItsTickerAndStopsWithTheContext(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	ctx := context.Background()
	_, err := h.pool.Exec(ctx, `
		INSERT INTO session (user_id, family_id, access_token_hash, access_expires_at,
		                     refresh_token_hash, refresh_expires_at, csrf_token_hash)
		VALUES (1, gen_random_uuid(), '\x11', now() - interval '99 days',
		        '\x12', now() - interval '99 days', '\x13')`)
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	s := auth.NewSweeper(h.repo, slog.New(slog.NewJSONHandler(io.Discard, nil)),
		50*time.Millisecond, 24*time.Hour)
	s.Start(runCtx)

	require.Eventually(t, func() bool {
		var n int
		if err := h.pool.QueryRow(ctx,
			`SELECT count(*) FROM session WHERE refresh_expires_at < now() - interval '30 days'`).Scan(&n); err != nil {
			return false
		}
		return n == 0
	}, 5*time.Second, 50*time.Millisecond, "the sweeper never deleted the long-dead session")

	// Cancelling must actually stop the loop, or a graceful shutdown hangs and
	// the pool closes underneath a live query.
	cancel()
	done := make(chan struct{})
	go func() { s.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("sweeper did not exit after its context was cancelled")
	}

	// The caller's live session is untouched.
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/links", nil).Code)
}

func TestSweeper_ClampsNonsenseIntervals(t *testing.T) {
	h := newHarness(t)
	// Zero/negative must fall back to the defaults rather than producing a
	// ticker that panics or spins.
	s := auth.NewSweeper(h.repo, slog.New(slog.NewJSONHandler(io.Discard, nil)), 0, 0)
	require.NotNil(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	cancel()
	s.Wait()
}

// ─────────────────────────────────────────────────────────────────────
// Invite listing
// ─────────────────────────────────────────────────────────────────────

func TestAdmin_ListInvitesShowsOpenOnesAndNeverTheToken(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	openTok := inviteToken(t, admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "open@example.com", "role": "editor"}))
	admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "expired@example.com", "role": "editor"})
	_, err := h.pool.Exec(context.Background(),
		`UPDATE invite SET expires_at = now() - interval '1 day' WHERE email_normalized = 'expired@example.com'`)
	require.NoError(t, err)

	rec := admin.do(http.MethodGet, "/api/admin/invites", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Contains(t, body, "open@example.com")
	assert.NotContains(t, body, "expired@example.com", "an expired invite is not an open one")
	// The raw token exists only in the response that MINTED the invite — the
	// database holds a sha256 and the server genuinely cannot reproduce it.
	assert.NotContains(t, body, openTok)
	assert.NotContains(t, body, "token_hash")
}

func TestAdmin_RevokeInviteIsIdempotentlyNotFound(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "gone@example.com", "role": "editor"})

	var id int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT id FROM invite WHERE email_normalized = 'gone@example.com'`).Scan(&id))

	assert.Equal(t, http.StatusNoContent,
		admin.do(http.MethodDelete, fmt.Sprintf("/api/admin/invites/%d", id), nil).Code)
	// A second revoke has nothing to act on and must not report success.
	assert.Equal(t, http.StatusNotFound,
		admin.do(http.MethodDelete, fmt.Sprintf("/api/admin/invites/%d", id), nil).Code)
	assert.Equal(t, http.StatusNotFound,
		admin.do(http.MethodDelete, "/api/admin/invites/99999", nil).Code)
}

func TestAdmin_InviteRejectsABadRoleOrEmail(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "ok@example.com", "role": "superuser"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_role", errCode(t, rec))

	rec = admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "not-an-email", "role": "editor"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_email", errCode(t, rec))
}

// An omitted role must default to `user`, never to admin.
func TestAdmin_InviteDefaultsToTheLeastPrivilege(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	rec := admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "default@example.com"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "editor", decode(t, rec)["role"])
}

// The grace window must not be an unbounded session factory.
//
// A consumed refresh token can be re-presented as fast as the network allows
// for the whole 10-second window. Without a cap, each replay mints another
// sibling — unbounded row creation from ONE token, which is both a
// storage-amplification vector and a way for a stolen token to fan out into
// many independently usable sessions. Past the cap the replay is treated as
// what it looks like.
func TestRefresh_GraceWindowCannotMintUnboundedSiblings(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	stale := c.cookies[auth.CookieRefresh]
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	// Hammer the same consumed token. Early replays are legitimate racing tabs;
	// once the family is full the request must be refused, not served.
	refused := false
	for range 12 {
		tab := h.client(t)
		tab.cookies[auth.CookieRefresh] = stale
		rec := tab.do(http.MethodPost, "/api/auth/refresh", nil)
		if rec.Code == http.StatusUnauthorized {
			assert.Equal(t, "session_revoked", errCode(t, rec))
			refused = true
			break
		}
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	require.True(t, refused, "replaying one consumed token 12 times must eventually be refused")

	var live int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session WHERE revoked_at IS NULL`).Scan(&live))
	assert.Zero(t, live, "hitting the cap revokes the whole family, it does not merely stop growing")
	var usedTokens int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session_used_token`).Scan(&usedTokens))
	assert.Zero(t, usedTokens, "cap teardown must purge the family's consumed-token trail")
	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, "/api/links", nil).Code)
}

// A couple of genuinely racing tabs must still work — the cap must not be so
// tight that it breaks the case the grace window exists for.
func TestRefresh_GraceWindowStillToleratesSeveralRacingTabs(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	stale := c.cookies[auth.CookieRefresh]
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)

	for i := range 3 {
		tab := h.client(t)
		tab.cookies[auth.CookieRefresh] = stale
		rec := tab.do(http.MethodPost, "/api/auth/refresh", nil)
		require.Equal(t, http.StatusOK, rec.Code, "racing tab %d must be served: %s", i+1, rec.Body.String())
		assert.Equal(t, http.StatusOK, tab.do(http.MethodGet, "/api/links", nil).Code)
	}
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "/api/links", nil).Code)
}

// ─────────────────────────────────────────────────────────────────────
// In-memory cache eviction
// ─────────────────────────────────────────────────────────────────────

// The rate-limit buckets are keyed by attacker-supplied e-mail on an
// UNAUTHENTICATED endpoint, so without eviction every address ever tried leaves
// a permanent entry. This is the wiring test: the method existing is not enough,
// something has to call it.
func TestSweeper_EvictsTheInMemoryCaches(t *testing.T) {
	h := newHarness(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	limiterCalls := make(chan time.Duration, 4)
	touchCalls := make(chan time.Duration, 4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := auth.NewSweeper(h.repo, logger, 40*time.Millisecond, 24*time.Hour).
		WithInMemory(
			func(d time.Duration) int { limiterCalls <- d; return 1 },
			func(d time.Duration) int { touchCalls <- d; return 2 },
		)
	s.Start(ctx)

	select {
	case d := <-limiterCalls:
		assert.Positive(t, d, "the sweep must be given a retention window")
	case <-time.After(3 * time.Second):
		t.Fatal("the rate-limit buckets were never swept — the leak the limiter documents is real")
	}
	select {
	case <-touchCalls:
	case <-time.After(3 * time.Second):
		t.Fatal("the last_seen_at throttle map was never swept")
	}

	cancel()
	s.Wait()
}

// A database error must not stop the in-memory pruning: those caches grow
// whether or not the DELETE succeeded.
func TestSweeper_PrunesMemoryEvenWhenTheDatabaseSweepFails(t *testing.T) {
	h := newHarness(t)
	h.pool.Close() // every subsequent query fails

	called := make(chan struct{}, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := auth.NewSweeper(h.repo, slog.New(slog.NewJSONHandler(io.Discard, nil)),
		40*time.Millisecond, 24*time.Hour).
		WithInMemory(func(time.Duration) int { called <- struct{}{}; return 0 })
	s.Start(ctx)

	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("a failing DB sweep must not skip the in-memory prune")
	}
	cancel()
	s.Wait()
}

// ─────────────────────────────────────────────────────────────────────
// Degradation when the database is gone
// ─────────────────────────────────────────────────────────────────────

// Every handler must answer a clean 500 envelope when the database is
// unreachable — never a panic, and never a leaked pgx string.
//
// CLAUDE.md §7 says pgx errors never reach clients, and these are the paths
// that would break it: each one logs the driver error and writes a generic
// envelope, and the whole family of `if err != nil` branches is otherwise
// unexercised. Closing the pool is the cheapest honest way to reach them all —
// the alternative is a fault-injecting driver wrapper for what is mechanical
// error handling.
func TestHandlers_DegradeCleanlyWhenTheDatabaseIsUnreachable(t *testing.T) {
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))
	h := newHarnessWith(t, pool, harnessOpts{TwoFactor: true, SMTP: true})
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	// Snapshot the cookies while the session still resolves, so the requests
	// below carry a credential and reach the handler body rather than stopping
	// at the middleware.
	h.pool.Close()

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"bootstrap status", http.MethodGet, "/api/auth/bootstrap-status", nil},
		{"me", http.MethodGet, "/api/auth/me", nil},
		{"login", http.MethodPost, "/api/auth/login", map[string]string{
			"email": "admin@example.com", "password": "a good password"}},
		{"refresh", http.MethodPost, "/api/auth/refresh", nil},
		{"invite lookup", http.MethodGet, "/api/auth/invites/sometoken", nil},
		{"accept invite", http.MethodPost, "/api/auth/invites/accept", map[string]string{
			"token": "t", "name": "n", "password": "a good password"}},
		{"admin list users", http.MethodGet, "/api/admin/users", nil},
		{"admin list invites", http.MethodGet, "/api/admin/invites", nil},
		{"admin create invite", http.MethodPost, "/api/admin/invites", map[string]string{
			"email": "someone@example.com", "role": "editor"}},
		{"admin update user", http.MethodPatch, "/api/admin/users/2", map[string]string{"name": "x"}},
		{"admin delete user", http.MethodDelete, "/api/admin/users/2", nil},
		{"admin revoke sessions", http.MethodPost, "/api/admin/users/2/sessions/revoke", nil},
		{"sessions", http.MethodGet, "/api/auth/sessions", nil},
		{"logout all", http.MethodPost, "/api/auth/logout-all", nil},
		{"change password", http.MethodPost, "/api/auth/password/change", map[string]string{
			"current_password": "a good password", "new_password": "another good password"}},

		// The PR3 surface. Same contract: a dead database is a clean refusal,
		// never a partial success and never a driver string on the wire.
		{"reset password", http.MethodPost, "/api/auth/password/reset", map[string]string{
			"token": "t", "password": "a good password"}},
		{"2fa verify", http.MethodPost, "/api/auth/2fa/verify", map[string]string{"code": "123456"}},
		{"2fa email", http.MethodPost, "/api/auth/2fa/email", nil},
		{"2fa totp start", http.MethodPost, "/api/auth/2fa/totp/start", map[string]string{
			"password": "a good password"}},
		{"2fa totp qr", http.MethodGet, "/api/auth/2fa/totp/qr.png", nil},
		{"2fa totp confirm", http.MethodPost, "/api/auth/2fa/totp/confirm", map[string]string{
			"code": "123456"}},
		{"2fa status", http.MethodGet, "/api/auth/2fa", nil},
		{"2fa totp disable", http.MethodPost, "/api/auth/2fa/totp/disable", map[string]string{
			"password": "a good password", "code": "123456"}},
		{"2fa regenerate codes", http.MethodPost, "/api/auth/2fa/recovery-codes/regenerate",
			map[string]string{"password": "a good password", "code": "123456"}},
		{"email verify", http.MethodPost, "/api/auth/email/verify", map[string]string{
			"code": "123456"}},
		{"email resend", http.MethodPost, "/api/auth/email/resend", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Must not panic — the Recoverer would turn that into a 500 too, so
			// assert on the body shape rather than only the status.
			rec := c.do(tc.method, tc.path, tc.body)

			assert.GreaterOrEqual(t, rec.Code, 400, "a dead database cannot produce a success")
			body := rec.Body.String()
			for _, leak := range []string{"pgx", "pgconn", "conn closed", "closed pool", "dial tcp", "sql:"} {
				assert.NotContains(t, body, leak,
					"%s leaked driver internals to the client: %s", tc.name, body)
			}
		})
	}
}

// /password/forgot is the ONE endpoint that must still answer 202 with the
// database gone.
//
// Its contract is "the response never depends on what the server found", and a
// database outage is exactly the kind of thing that would otherwise turn it
// into an oracle: a 500 for a lookup that failed and a 202 for one that
// succeeded would separate the two cases the endpoint exists to blur.
func TestForgotPassword_StillAnswers202WithoutADatabase(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	h.pool.Close()

	rec := h.client(t).do(http.MethodPost, "/api/auth/password/forgot",
		map[string]string{"email": "admin@example.com"})
	assert.Equal(t, http.StatusAccepted, rec.Code,
		"a database outage made the reset endpoint distinguishable")
}

// Logout must succeed even with no database at all. It is the one operation
// where the only wrong outcome is the user's cookies surviving, so it clears
// them first and treats the revocation as best-effort.
func TestLogout_SucceedsWithoutADatabase(t *testing.T) {
	h := newHarness(t)
	c := h.bootstrapAdmin(t, "admin@example.com", "a good password")
	h.pool.Close()

	rec := c.do(http.MethodPost, "/api/auth/logout", nil)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	cleared := 0
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 {
			cleared++
		}
	}
	assert.GreaterOrEqual(t, cleared, 3, "the cookies must be cleared regardless of the database")
}

// The last-admin guard must hold under CONCURRENCY, not just sequentially.
//
// It is a read-then-write check — count the admins, then demote one — so with
// the count outside the write's transaction, two simultaneous demotions both
// observe "2" and both proceed, leaving ZERO active administrators. That state
// is unrecoverable through the API: nobody can sign in with the privilege
// needed to fix it, so it takes a direct database edit.
//
// The shape that actually reaches zero is TWO admins demoting EACH OTHER at the
// same time — not one admin demoting two others, because an admin cannot demote
// themselves (the self-target guard) and so always survives their own sweep.
// Getting that wrong makes the test vacuous: it passes whether or not the guard
// is atomic.
func TestAdmin_MutualConcurrentDemotionCannotStrandTheInstanceWithNoAdmin(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "alice@example.com", "a good password")
	bob := testdb.SeedUserWithPassword(t, h.pool, "bob@example.com", "a good password", "admin")

	// Alice bootstraps as OWNER, and the owner is immutable through the API.
	// Levelling her to admin is what makes this a race between two peers, which
	// is the only shape where the counting guard is what decides.
	_, err := h.pool.Exec(context.Background(),
		`UPDATE app_user SET role = 'admin' WHERE role = 'owner'`)
	require.NoError(t, err)

	// Exactly two active admins now: alice (id 1) and bob.
	var before int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user WHERE role = 'admin' AND status = 'active'`).Scan(&before))
	require.Equal(t, 2, before, "fixture precondition: exactly two admins")

	alice := h.client(t)
	require.Equal(t, http.StatusOK, alice.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "alice@example.com", "password": "a good password"}).Code)
	bobCl := h.client(t)
	require.Equal(t, http.StatusOK, bobCl.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "bob@example.com", "password": "a good password"}).Code)

	// Alice demotes Bob while Bob demotes Alice. Neither is self-targeting, so
	// only the last-admin guard can stop them both landing.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		codes[0] = alice.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(bob)),
			map[string]string{"role": "editor"}).Code
	}()
	go func() {
		defer wg.Done()
		<-start
		codes[1] = bobCl.do(http.MethodPatch, "/api/admin/users/1",
			map[string]string{"role": "editor"}).Code
	}()
	close(start) // release both as close to simultaneously as Go allows
	wg.Wait()

	var admins int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user WHERE role = 'admin' AND status = 'active'`).Scan(&admins))
	assert.GreaterOrEqual(t, admins, 1,
		"both demotions were accepted (codes %v) and the instance has no administrator left — "+
			"unrecoverable without direct SQL", codes)
	// Exactly one must have been refused. Which refusal is correct depends on
	// who won: the loser either trips the last-admin guard (409) or, if their
	// own demotion landed first, has already lost the role and is refused by
	// RequireAdmin (404, never 403 — see the middleware).
	refused := 0
	for _, code := range codes {
		if code == http.StatusConflict || code == http.StatusNotFound {
			refused++
		}
	}
	assert.Equal(t, 1, refused,
		"exactly one of the two mutual demotions must be refused, got %v", codes)
}

// The sequential case must still produce the documented error.
func TestAdmin_DemotingTheLastAdminIsRefused(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin(t, "admin@example.com", "a good password")
	other := testdb.SeedUserWithPassword(t, h.pool, "admin2@example.com", "a good password", "admin")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "admin2@example.com", "password": "a good password",
	}).Code)
	_ = other

	// Clear the OWNER out of the way in SQL: the seat is immutable through the
	// API by design, and what this test needs is an instance whose only
	// administrator is an ordinary admin — the state where the counting guard,
	// rather than owner immutability, is what refuses.
	_, err := h.pool.Exec(context.Background(),
		`UPDATE app_user SET role = 'editor' WHERE role = 'owner'`)
	require.NoError(t, err)

	// Now admin2 is the only one, and cannot be demoted even by itself.
	rec := c.do(http.MethodPatch, fmt.Sprintf("/api/admin/users/%d", int64(other)),
		map[string]string{"role": "editor"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	// The self-target guard fires first here, which is also correct — both
	// refuse, and neither leaves the instance without an administrator.
	assert.Contains(t, []string{"self_target", "last_admin"}, errCode(t, rec))
}

// The complete administration lifecycle, end to end.
//
// Each step is covered in isolation elsewhere; what this adds is the ORDER —
// that an invited user can be listed, renamed, disabled, re-enabled and deleted
// without any step corrupting the next, and that disabling really does sever
// access mid-session while re-enabling restores the ability to sign in again.
func TestAdmin_FullUserLifecycle(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	// 1. Invite, and confirm it shows up as open.
	tok := inviteToken(t, admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "member@example.com", "role": "editor"}))
	rec := admin.do(http.MethodGet, "/api/admin/invites", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "member@example.com")

	// 2. Accept it. The new account is signed in immediately.
	member := h.client(t)
	rec = member.do(http.MethodPost, "/api/auth/invites/accept", map[string]string{
		"token": tok, "name": "Member", "password": "a fresh new password"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	memberID := int64(decode(t, rec)["user"].(map[string]any)["id"].(float64))
	require.Equal(t, http.StatusOK, member.do(http.MethodGet, "/api/links", nil).Code)

	// The invite is consumed, so it no longer appears as open.
	rec = admin.do(http.MethodGet, "/api/admin/invites", nil)
	assert.NotContains(t, rec.Body.String(), "member@example.com")

	// 3. Both accounts are listed, with no secret material.
	rec = admin.do(http.MethodGet, "/api/admin/users", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, decode(t, rec)["users"], 2)
	assert.NotContains(t, rec.Body.String(), "$2a$")

	path := fmt.Sprintf("/api/admin/users/%d", memberID)

	// 4. Rename. A name-only edit must not disturb role or status.
	rec = admin.do(http.MethodPatch, path, map[string]string{"name": "Renamed Member"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Equal(t, "Renamed Member", body["name"])
	assert.Equal(t, "editor", body["role"])
	assert.Equal(t, "active", body["status"])
	assert.Equal(t, http.StatusOK, member.do(http.MethodGet, "/api/links", nil).Code,
		"a rename must not sign the user out")

	// 5. Disable — the live session dies at once, not at the next token expiry.
	require.Equal(t, http.StatusOK,
		admin.do(http.MethodPatch, path, map[string]string{"status": "disabled"}).Code)
	assert.Equal(t, http.StatusUnauthorized, member.do(http.MethodGet, "/api/links", nil).Code)
	assert.Equal(t, http.StatusUnauthorized, h.client(t).do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "member@example.com", "password": "a fresh new password"}).Code)

	// 6. Re-enable — signing in works again.
	require.Equal(t, http.StatusOK,
		admin.do(http.MethodPatch, path, map[string]string{"status": "active"}).Code)
	revived := h.client(t)
	require.Equal(t, http.StatusOK, revived.do(http.MethodPost, "/api/auth/login",
		map[string]string{"email": "member@example.com", "password": "a fresh new password"}).Code)

	// 7. Promote to admin, then delete. Deleting an admin is allowed while
	//    another active admin remains.
	require.Equal(t, http.StatusOK,
		admin.do(http.MethodPatch, path, map[string]string{"role": "admin"}).Code)
	require.Equal(t, http.StatusNoContent, admin.do(http.MethodDelete, path, nil).Code)

	rec = admin.do(http.MethodGet, "/api/admin/users", nil)
	assert.Len(t, decode(t, rec)["users"], 1)
	assert.Equal(t, http.StatusUnauthorized, revived.do(http.MethodGet, "/api/links", nil).Code,
		"deleting the account must invalidate its sessions")
}

// Bootstrap must report a conflict, not a 500, when the address it is handed
// already belongs to some other row.
//
// Reachable because "no ACTIVE account yet" and "this e-mail is free" are
// different questions: a pending or disabled row can hold the address while the
// instance still looks unconfigured. The unique index is what catches it, and
// the handler has to translate that into 409 rather than letting a driver error
// surface.
func TestBootstrap_ConflictsWhenTheEmailBelongsToAnotherRow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A non-active row already holding the address.
	_, err := h.pool.Exec(ctx, `
		INSERT INTO app_user (email, email_normalized, name, role, status)
		VALUES ('taken@example.com', 'taken@example.com', 'Squatter', 'editor', 'disabled')`)
	require.NoError(t, err)
	// Plus the placeholder bootstrap would otherwise claim.
	_, err = h.pool.Exec(ctx, `
		INSERT INTO app_user (email, email_normalized, name, role, status)
		VALUES ('admin@foldex.local', 'admin@foldex.local', 'Administrator', 'admin', 'pending')`)
	require.NoError(t, err)

	rec := h.client(t).do(http.MethodPost, "/api/auth/bootstrap", map[string]string{
		"email": "taken@example.com", "name": "Ana", "password": "correct horse battery",
	})

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "email_taken", errCode(t, rec))
	assert.NotContains(t, rec.Body.String(), "23505", "the driver's constraint code must not reach the client")
}

// Accepting an invite must report a conflict when the address was claimed
// between the invitation and the acceptance.
//
// CreateInvite refuses an address that already has an account, so this can only
// happen in the gap: the invite goes out, someone else creates that account,
// and then the link is clicked. The insert loses to the unique index, and the
// handler must turn that into 409 instead of 500.
func TestAcceptInvite_ConflictsWhenTheAddressWasClaimedMeanwhile(t *testing.T) {
	h := newHarness(t)
	admin := h.bootstrapAdmin(t, "admin@example.com", "a good password")

	tok := inviteToken(t, admin.do(http.MethodPost, "/api/admin/invites",
		map[string]string{"email": "racer@example.com", "role": "editor"}))

	// The address is taken after the invite was issued.
	testdb.SeedUserWithPassword(t, h.pool, "racer@example.com", "some other password", "editor")

	rec := h.client(t).do(http.MethodPost, "/api/auth/invites/accept", map[string]string{
		"token": tok, "name": "Racer", "password": "a fresh new password",
	})

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "email_taken", errCode(t, rec))

	// The invite must NOT be consumed by a failed acceptance — the transaction
	// rolls back, so it stays usable if the collision is resolved.
	var accepted *string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT accepted_at::text FROM invite WHERE email_normalized = 'racer@example.com'`).Scan(&accepted))
	assert.Nil(t, accepted, "a failed acceptance must not consume the invitation")
}

// TestAdmin_ConcurrentMutualDemotionCannotEmptyTheAdminSet drives the guard at
// the REPOSITORY level, which is the only layer where the race it defends
// against actually exists.
//
// The HTTP-level sibling of this test (above) is worth keeping — it proves the
// handler surfaces the right status codes — but it cannot reliably force the
// interleaving: each request first resolves a session, and that variable work
// happens before UpdateUser opens its transaction, so the two critical
// sections rarely overlap. Calling UpdateUser directly puts the barrier
// immediately before the transaction, where a missing advisory lock has
// nowhere to hide: both transactions would count two admins, both would demote,
// and the instance would be left with none — a state only direct SQL can undo.
//
// Rounds, rather than a single attempt, because the failure being excluded is
// probabilistic: one lucky serialisation would let a broken guard pass.
func TestAdmin_ConcurrentMutualDemotionCannotEmptyTheAdminSet(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	userRole := authctx.RoleEditor

	const rounds = 20
	for round := range rounds {
		// The guard counts every active admin, so the round is only meaningful
		// when these two are the only ones. Survivors of earlier rounds are
		// demoted rather than deleted (cheaper, and it keeps their ids taken).
		// Clears OWNER as well as admin: the guard counts every role that can
		// administer, so an owner left standing would keep the count above one
		// and neither demotion would ever be refused — the test would pass
		// while proving nothing.
		_, err := h.pool.Exec(ctx,
			`UPDATE app_user SET role = 'editor' WHERE role IN ('owner', 'admin')`)
		require.NoError(t, err)
		a := testdb.SeedUser(t, h.pool, fmt.Sprintf("race-a%d@example.com", round), "admin")
		b := testdb.SeedUser(t, h.pool, fmt.Sprintf("race-b%d@example.com", round), "admin")

		var errs [2]error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, errs[0] = h.repo.UpdateUser(ctx, a, nil, &userRole, nil)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, errs[1] = h.repo.UpdateUser(ctx, b, nil, &userRole, nil)
		}()
		close(start)
		wg.Wait()

		var admins int
		require.NoError(t, h.pool.QueryRow(ctx,
			`SELECT count(*) FROM app_user WHERE role IN ('owner', 'admin') AND status = 'active'`).Scan(&admins))
		require.GreaterOrEqual(t, admins, 1,
			"round %d: both demotions landed (errs=%v) — no administrator left", round, errs)

		refused := 0
		for _, e := range errs {
			if errors.Is(e, auth.ErrLastAdmin) {
				refused++
			}
		}
		require.Equal(t, 1, refused,
			"round %d: exactly one demotion must trip ErrLastAdmin, got %v", round, errs)
	}
}

// A live session and a half-finished login are mutually exclusive states, so
// establishing one must end the other.
//
// Before this, only the 2FA path cleared fx_pa — the password and OAuth paths
// did not, so a challenge abandoned mid-flight left its cookie sitting beside a
// fresh session for the rest of its TTL. Not redeemable without the code, which
// is why it is hardening rather than a hole; the point is that the guarantee
// now comes from SetSession, the one function that defines "signed in on the
// wire", instead of from four call sites each remembering.
func TestLogin_ClearsAStalePreAuthCookie(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{TwoFactor: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	testdb.SeedUserWithPassword(t, h.pool, "2fa@example.com", "a good password", "editor")
	enrolUser(t, h, "2fa@example.com", "a good password")

	// A FRESH client, so the enrollment's own session does not mask the
	// behaviour under test. Its login stops at the second factor, leaving fx_pa.
	c := h.client(t)
	rec := c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "2fa@example.com", "password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "two_factor_required", decode(t, rec)["status"])
	require.NotEmpty(t, c.cookies[auth.CookiePreAuth], "the challenge must have set fx_pa")

	// Abandon it and sign in as an account that needs no second factor.
	testdb.SeedUserWithPassword(t, h.pool, "plain@example.com", "a good password", "editor")
	rec = c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "plain@example.com", "password": "a good password",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "authenticated", decode(t, rec)["status"])

	pa := cookieByName(rec, auth.CookiePreAuth)
	require.NotNil(t, pa, "the response must expire fx_pa rather than ignore it")
	assert.Less(t, pa.MaxAge, 0, "fx_pa must be expired, not refreshed")
	// Cleared with the SAME path it was set with: a browser keys cookies by
	// (name, domain, path), so expiring it at "/" would leave the real one at
	// /api/auth untouched.
	assert.Equal(t, "/api/auth", pa.Path)
	assert.Empty(t, c.cookies[auth.CookiePreAuth], "the client must no longer hold one")
}

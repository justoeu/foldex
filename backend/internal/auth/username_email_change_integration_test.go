//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"foldex/internal/auth"
	"foldex/internal/testdb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────
// Username
// ─────────────────────────────────────────────────────────────────────

func TestUsername_SetsAndSignsIn(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")

	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)

	rec := c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"username": "Valmir"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// Stored NORMALIZED: the login lookup compares against the lowercase form,
	// so a stored `Valmir` would be a username its owner could never use.
	// The typed casing survives; only the LOOKUP column is folded. Writing the
	// normalized value into both would make the column pair describe nothing.
	assert.Equal(t, "Valmir", decode(t, rec)["user"].(map[string]any)["username"])
	var stored, normalized string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT username, username_normalized FROM app_user WHERE email = 'user@example.com'`).
		Scan(&stored, &normalized))
	assert.Equal(t, "Valmir", stored)
	assert.Equal(t, "valmir", normalized)

	// The whole point: the same credential, named the other way.
	fresh := h.client(t)
	require.Equal(t, http.StatusOK, fresh.do(http.MethodPost, "/api/auth/login", map[string]string{
		"identifier": "valmir", "password": "a good password",
	}).Code)

	// Case and surrounding space are the user's, not the lookup's.
	again := h.client(t)
	require.Equal(t, http.StatusOK, again.do(http.MethodPost, "/api/auth/login", map[string]string{
		"identifier": "  VALMIR ", "password": "a good password",
	}).Code)
}

// A username shaped like an address would live in the same namespace as
// everybody's mailbox: claim someone else's address as your username and their
// password attempts arrive at your account.
func TestUsername_CannotBeShapedLikeAnAddress(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)

	for _, bad := range []string{"victim@example.com", "a@b", "ab", "no spaces here", "-leading", "trailing-",
		"admin", "root", "waaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaay-too-long"} {
		rec := c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"username": bad})
		assert.Equalf(t, http.StatusBadRequest, rec.Code, "username %q was accepted", bad)
	}
}

func TestUsername_IsUniqueAcrossAccounts(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "one@example.com", "a good password", "editor")
	testdb.SeedUserWithPassword(t, h.pool, "two@example.com", "a good password", "editor")

	first := h.client(t)
	require.Equal(t, http.StatusOK, first.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "one@example.com", "password": "a good password",
	}).Code)
	require.Equal(t, http.StatusOK, first.do(http.MethodPatch, "/api/auth/profile",
		map[string]string{"username": "shared"}).Code)

	second := h.client(t)
	require.Equal(t, http.StatusOK, second.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "two@example.com", "password": "a good password",
	}).Code)
	rec := second.do(http.MethodPatch, "/api/auth/profile", map[string]string{"username": "shared"})
	// 409, not 500: the unique index is matched by CONSTRAINT NAME so the
	// refusal stays a refusal even behind a layer that rewraps the error.
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "username_taken", errCode(t, rec))
}

// Clearing is a real operation. Without it an account that set one would have
// to be handed to an administrator to get rid of it.
func TestUsername_CanBeCleared(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)
	require.Equal(t, http.StatusOK, c.do(http.MethodPatch, "/api/auth/profile",
		map[string]string{"username": "temporary"}).Code)

	rec := c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"username": ""})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", decode(t, rec)["user"].(map[string]any)["username"])

	// And the name stops being a way in the moment it is cleared.
	after := h.client(t)
	assert.Equal(t, http.StatusUnauthorized, after.do(http.MethodPost, "/api/auth/login",
		map[string]string{"identifier": "temporary", "password": "a good password"}).Code)
}

// A rename must not carry the username along. Both fields are tri-state for the
// same reason, and replaying a cached value is how a change made in another tab
// gets silently undone.
func TestUsername_IsUntouchedByAPlainRename(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)
	require.Equal(t, http.StatusOK, c.do(http.MethodPatch, "/api/auth/profile",
		map[string]string{"username": "keeper"}).Code)

	rec := c.do(http.MethodPatch, "/api/auth/profile", map[string]string{"name": "New Name"})
	require.Equal(t, http.StatusOK, rec.Code)
	user := decode(t, rec)["user"].(map[string]any)
	assert.Equal(t, "keeper", user["username"])
	assert.Equal(t, "New Name", user["name"])
}

// Both names have to charge ONE budget. Keyed by the string the caller typed, an
// attacker alternates the two and gets double the attempts while the per-account
// cap still reads as five.
func TestUsername_SharesTheLoginBudgetWithTheAddress(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "user@example.com", "a good password", "editor")
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "user@example.com", "password": "a good password",
	}).Code)
	require.Equal(t, http.StatusOK, c.do(http.MethodPatch, "/api/auth/profile",
		map[string]string{"username": "target"}).Code)

	// Spend the budget under the ADDRESS...
	var last int
	for i := 0; i < 8; i++ {
		last = h.client(t).do(http.MethodPost, "/api/auth/login", map[string]string{
			"email": "user@example.com", "password": "wrong password",
		}).Code
		if last == http.StatusTooManyRequests {
			break
		}
	}
	require.Equal(t, http.StatusTooManyRequests, last, "the per-account bucket never locked")

	// ...and the USERNAME must find it already spent.
	assert.Equal(t, http.StatusTooManyRequests, h.client(t).do(http.MethodPost, "/api/auth/login",
		map[string]string{"identifier": "target", "password": "wrong password"}).Code)
}

// ─────────────────────────────────────────────────────────────────────
// E-mail change
// ─────────────────────────────────────────────────────────────────────

func changeTokenFrom(t *testing.T, body string) string {
	return tokenFromFragment(t, body, "email-change")
}

func TestEmailChange_OnlyMovesAfterTheNewAddressConfirms(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	rec := c.do(http.MethodPost, "/api/auth/email/change", map[string]string{
		"new_email": "new@example.com", "password": "a good password",
	})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	// Nothing has moved yet — that is the entire point of the two-step.
	var live string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email FROM app_user WHERE id = 1`).Scan(&live))
	assert.Equal(t, "old@example.com", live)

	// The OLD address is warned, and its message carries NO link: its reader
	// may be someone whose account is being taken by a person who already has
	// their session, and "click here to stop it" is the forgery's own sentence.
	notice := h.mail.waitFor(t, "old@example.com")
	assert.Contains(t, notice.Text, "new@example.com")
	assert.NotContains(t, notice.Text, "http://")
	assert.NotContains(t, notice.Text, "https://")
	// `<a ` alone is not enough: the URL box renders a link as selectable TEXT
	// with no anchor, which is exactly the shape a careless refactor produces.
	assert.NotContains(t, notice.HTML, "<a ")
	assert.NotContains(t, notice.HTML, "http://")
	assert.NotContains(t, notice.HTML, "https://")

	token := changeTokenFrom(t, h.mail.waitFor(t, "new@example.com").Text)
	// From a client with NO session: the mailbox being moved to is very often
	// read on a device that never signed in.
	confirmRec := h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": token})
	require.Equal(t, http.StatusNoContent, confirmRec.Code, confirmRec.Body.String())

	var moved string
	var verified *string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email, email_verified_at::text FROM app_user WHERE id = 1`).Scan(&moved, &verified))
	assert.Equal(t, "new@example.com", moved)
	// Following the link IS the proof of control; asking again would demand the
	// user prove what they just proved.
	assert.NotNil(t, verified)

	// The identifier moved, so every session issued against the old one died.
	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, "/api/links", nil).Code)

	// And the new address is the way in.
	after := h.client(t)
	assert.Equal(t, http.StatusOK, after.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "new@example.com", "password": "a good password",
	}).Code)
}

func TestEmailChange_RefusesAnAddressAnotherAccountHolds(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "taken@example.com", "a good password", "editor")

	rec := c.do(http.MethodPost, "/api/auth/email/change", map[string]string{
		"new_email": "taken@example.com", "password": "a good password",
	})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "email_taken", errCode(t, rec))
}

// A live session is not enough. Without the password, a stolen cookie moves the
// account's recovery channel to an address the attacker owns.
func TestEmailChange_RequiresTheCurrentPassword(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	rec := c.do(http.MethodPost, "/api/auth/email/change", map[string]string{
		"new_email": "new@example.com", "password": "not the password",
	})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, h.drainMail(t), "a refused request must not queue a link")
}

// The epoch binding: a password change between the request and the click kills
// the pending move, exactly as it kills a stale reset link.
func TestEmailChange_DiesWhenTheCredentialEpochMoves(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "new@example.com", "password": "a good password"}).Code)
	token := changeTokenFrom(t, h.mail.waitFor(t, "new@example.com").Text)

	require.Equal(t, http.StatusNoContent, c.do(http.MethodPost, "/api/auth/password/change",
		map[string]string{
			"current_password": "a good password",
			"new_password":     "another good password",
		}).Code)

	assert.Equal(t, http.StatusNotFound, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": token}).Code)

	var live string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email FROM app_user WHERE id = 1`).Scan(&live))
	assert.Equal(t, "old@example.com", live)
}

func TestEmailChange_TokenIsSingleUse(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "new@example.com", "password": "a good password"}).Code)
	token := changeTokenFrom(t, h.mail.waitFor(t, "new@example.com").Text)

	require.Equal(t, http.StatusNoContent, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": token}).Code)
	assert.Equal(t, http.StatusNotFound, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": token}).Code)
}

// Asking twice must leave ONE live link. Two would mean two mailboxes each able
// to take the account, and the owner has only seen the second one.
func TestEmailChange_SecondRequestSupersedesTheFirst(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "typo@example.com", "password": "a good password"}).Code)
	first := changeTokenFrom(t, h.mail.waitFor(t, "typo@example.com").Text)

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "right@example.com", "password": "a good password"}).Code)
	second := changeTokenFrom(t, h.mail.waitFor(t, "right@example.com").Text)

	assert.Equal(t, http.StatusNotFound, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": first}).Code,
		"the superseded link still worked")
	require.Equal(t, http.StatusNoContent, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": second}).Code)
}

func TestEmailChange_PendingIsReadableAndCancellable(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "new@example.com", "password": "a good password"}).Code)
	token := changeTokenFrom(t, h.mail.waitFor(t, "new@example.com").Text)

	rec := c.do(http.MethodGet, "/api/auth/email/change", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	pending, ok := decode(t, rec)["pending"].(map[string]any)
	require.True(t, ok, "a live request must be readable after a reload")
	assert.Equal(t, "new@example.com", pending["new_email"])

	require.Equal(t, http.StatusNoContent, c.do(http.MethodDelete, "/api/auth/email/change", nil).Code)
	assert.Nil(t, decode(t, c.do(http.MethodGet, "/api/auth/email/change", nil))["pending"])
	assert.Equal(t, http.StatusNotFound, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": token}).Code,
		"cancelling left the link alive")
}

// The log driver prints the body to stdout, and this link MOVES the account —
// the same refusal the second factor and administrator recovery already make.
func TestEmailChange_RefusedWhenMailOnlyGoesToTheLog(t *testing.T) {
	h := newHarness(t)
	testdb.SeedUserWithPassword(t, h.pool, "old@example.com", "a good password", "editor")
	c := h.client(t)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "old@example.com", "password": "a good password",
	}).Code)

	rec := c.do(http.MethodPost, "/api/auth/email/change", map[string]string{
		"new_email": "new@example.com", "password": "a good password",
	})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "mail_unavailable", errCode(t, rec))
}

func TestEmailChange_RefusesTheAddressTheAccountAlreadyHas(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	rec := c.do(http.MethodPost, "/api/auth/email/change", map[string]string{
		"new_email": "OLD@example.com", "password": "a good password",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "email_unchanged", errCode(t, rec))
}

// An API token is a CONTENT credential. Moving the account's identity with one
// would make it the account, which is the line RejectAPIToken draws.
func TestEmailChange_IsClosedToAPITokens(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	token := h.mintToken(t, c, "extension")
	assert.Equal(t, http.StatusForbidden, h.client(t).doRaw(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "new@example.com", "password": "a good password"},
		bearerHeader(token)).Code)
}

// The address was validated by a second, weaker implementation that dropped the
// length bound: a 400-character address got a 202 here and then an opaque 500
// at the confirmation, to an unauthenticated caller with no way to correct it.
func TestEmailChange_RejectsAnAddressTheColumnCouldNotHold(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	long := strings.Repeat("a", 320) + "@example.com"
	rec := c.do(http.MethodPost, "/api/auth/email/change", map[string]string{
		"new_email": long, "password": "a good password",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "invalid_email", errCode(t, rec))
	assert.Empty(t, h.drainMail(t), "a refused address must not queue a link")

	var pending int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM email_change`).Scan(&pending))
	assert.Zero(t, pending, "a refused address must not leave a row behind")
}

// The form check had no test at all: a mutation loosening it would accept
// `a@.com`, persist the row and queue a message to a domain that cannot exist.
func TestEmailChange_RefusesAMalformedAddress(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	for _, bad := range []string{"not-an-address", "a@", "a@b", "@b.com", "a@b.", "a b@c.com"} {
		rec := c.do(http.MethodPost, "/api/auth/email/change", map[string]string{
			"new_email": bad, "password": "a good password",
		})
		assert.Equalf(t, http.StatusBadRequest, rec.Code, "address %q was accepted", bad)
	}
}

// The race the request-time check cannot close: somebody claims the address
// between the request and the click. It must answer 409 — a 500 would send a
// user with a working token to support over something they can fix themselves.
func TestEmailChange_ConfirmFailsWhenTheAddressWasClaimedMeanwhile(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "contested@example.com", "password": "a good password"}).Code)
	token := changeTokenFrom(t, h.mail.waitFor(t, "contested@example.com").Text)

	testdb.SeedUserWithPassword(t, h.pool, "contested@example.com", "a good password", "editor")

	rec := h.client(t).do(http.MethodPost, "/api/auth/email-change/confirm",
		map[string]string{"token": token})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "email_taken", errCode(t, rec))

	var live string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email FROM app_user WHERE id = 1`).Scan(&live))
	assert.Equal(t, "old@example.com", live, "a lost race must not move the address")
}

// Revoking ONE session does not bump the credential epoch, so the epoch check
// cannot see it. Someone who spots a strange device and revokes just that one —
// the proportionate response — would otherwise leave the pending move alive for
// whoever was on it.
func TestEmailChange_DiesWhenTheAuthorizingSessionIsRevoked(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "new@example.com", "password": "a good password"}).Code)
	token := changeTokenFrom(t, h.mail.waitFor(t, "new@example.com").Text)

	_, err := h.pool.Exec(context.Background(),
		`UPDATE session SET revoked_at = now(), revoked_reason = 'logout' WHERE revoked_at IS NULL`)
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": token}).Code)

	var live string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email FROM app_user WHERE id = 1`).Scan(&live))
	assert.Equal(t, "old@example.com", live)
}

// The DISPLAY column must keep the casing its owner typed; only the lookup
// column is folded. This was wrong on the e-mail while being right on the
// username one file over — the handler normalized before the repository, so
// both columns ended up holding the same lowercased string.
func TestEmailChange_KeepsTheCasingTheOwnerTyped(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	rec := c.do(http.MethodPost, "/api/auth/email/change", map[string]string{
		"new_email": "  Jane.Smith@Company.COM  ", "password": "a good password",
	})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	// What the screen echoes back is what the user typed, trimmed.
	assert.Equal(t, "Jane.Smith@Company.COM", decode(t, rec)["new_email"])

	var stored, normalized string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT new_email, new_email_normalized FROM email_change WHERE consumed_at IS NULL`).
		Scan(&stored, &normalized))
	assert.Equal(t, "Jane.Smith@Company.COM", stored)
	assert.Equal(t, "jane.smith@company.com", normalized)

	token := changeTokenFrom(t, h.mail.waitFor(t, "Jane.Smith@Company.COM").Text)
	require.Equal(t, http.StatusNoContent, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": token}).Code)

	var live, liveNorm string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email, email_normalized FROM app_user WHERE id = 1`).Scan(&live, &liveNorm))
	assert.Equal(t, "Jane.Smith@Company.COM", live)
	assert.Equal(t, "jane.smith@company.com", liveNorm)

	// ...and the folded value is still what signs in.
	after := h.client(t)
	assert.Equal(t, http.StatusOK, after.do(http.MethodPost, "/api/auth/login", map[string]string{
		"email": "JANE.SMITH@company.com", "password": "a good password",
	}).Code)
}

// The POSITIVE direction of the session binding, and the one worth locking:
// the guard above must kill a pending move when the authorizing session is
// revoked, and must NOT kill it for anything else. Ordinary refresh rotation is
// what makes that distinction load-bearing — a browser left open rotates on its
// own schedule, so a check that could not tell rotation from revocation would
// break the feature for everyone who takes more than fifteen minutes to walk to
// the other mailbox, and would do it silently: the confirm answers the same 404
// every other failure answers.
//
// What makes it survive is that rotation REWRITES the session row rather than
// replacing it, so the bound session_id still names a live row. That is an
// implementation detail the binding depends on, which is exactly why it is
// asserted here and not assumed.
func TestEmailChange_SurvivesOrdinaryRefreshRotation(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "new@example.com", "password": "a good password"}).Code)
	token := changeTokenFrom(t, h.mail.waitFor(t, "new@example.com").Text)

	var boundSession int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT session_id FROM email_change WHERE consumed_at IS NULL`).Scan(&boundSession))

	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code,
			"rotation %d", i+1)
	}

	var sessions int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session WHERE id = $1 AND revoked_at IS NULL`, boundSession).Scan(&sessions))
	require.Equal(t, 1, sessions, "rotation rewrites the bound row; it must not replace or revoke it")

	require.Equal(t, http.StatusNoContent, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": token}).Code)

	var live string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email FROM app_user WHERE id = 1`).Scan(&live))
	assert.Equal(t, "new@example.com", live)
}

// The grace window is the one rotation that does NOT rewrite in place: a second
// tab presenting the same refresh token within ten seconds gets a SIBLING
// session, a new row in the same family. The bound row survives that too — but
// only because the sibling is added beside it rather than taking its place, and
// a change to the grace path that revoked the original would take pending moves
// down with it for anyone who had two tabs open.
func TestEmailChange_SurvivesTheGraceWindowSibling(t *testing.T) {
	h := newHarnessWith(t, testdb.Shared(t), harnessOpts{SMTP: true})
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	c := h.bootstrapAdmin(t, "old@example.com", "a good password")
	firstRT := c.cookies[auth.CookieRefresh]

	h.mail.reset()
	require.Equal(t, http.StatusAccepted, c.do(http.MethodPost, "/api/auth/email/change",
		map[string]string{"new_email": "new@example.com", "password": "a good password"}).Code)
	token := changeTokenFrom(t, h.mail.waitFor(t, "new@example.com").Text)

	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/api/auth/refresh", nil).Code)
	tab2 := h.client(t)
	tab2.cookies[auth.CookieRefresh] = firstRT
	require.Equal(t, http.StatusOK, tab2.do(http.MethodPost, "/api/auth/refresh", nil).Code,
		"the replay inside the grace window must be served, not treated as reuse")

	var sessions int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session WHERE revoked_at IS NULL`).Scan(&sessions))
	require.Equal(t, 2, sessions, "the sibling is added beside the bound row, not in place of it")

	require.Equal(t, http.StatusNoContent, h.client(t).do(http.MethodPost,
		"/api/auth/email-change/confirm", map[string]string{"token": token}).Code)

	var live string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT email FROM app_user WHERE id = 1`).Scan(&live))
	assert.Equal(t, "new@example.com", live)
}

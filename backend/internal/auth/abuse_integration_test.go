//go:build integration

package auth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/abusepolicy"
	"foldex/internal/auth"
	"foldex/internal/pkg/authctx/authctxtest"
	"foldex/internal/roleperm"
	"foldex/internal/testdb"
)

// seedFailure writes one failed sign-in from an address against a mailbox,
// backdated so a test can place it inside or outside a window.
func seedFailure(t *testing.T, h *harness, ip, target string, ago time.Duration) {
	t.Helper()
	ctx := context.Background()
	_, err := h.pool.Exec(ctx, `
		INSERT INTO audit_log (action, target_email, ip, ip_trusted, created_at)
		VALUES ($1, $2, $3::inet, false, now() - $4::interval)`,
		auth.AuditLoginFailed, target, ip, ago.String())
	require.NoError(t, err)
}

func seedThrottle(t *testing.T, h *harness, ip string, ago time.Duration) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO audit_log (action, ip, ip_trusted, created_at)
		VALUES ($1, $2::inet, false, now() - $3::interval)`,
		auth.AuditRateLimited, ip, ago.String())
	require.NoError(t, err)
}

func truncateTrail(t *testing.T, h *harness) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(), `TRUNCATE audit_log, ip_block RESTART IDENTITY`)
	require.NoError(t, err)
}

// ─────────────────────────────────────────────────────────────────────
// The policy surface
// ─────────────────────────────────────────────────────────────────────

// An instance that never opened the screen reports the compiled defaults, and
// the bounds travel with them so the form renders its own limits instead of
// keeping a second copy of these numbers in TypeScript.
func TestAbusePolicyAPI_GetCarriesThePolicyBoundsAndObservations(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	body := decode(t, owner.do(http.MethodGet, "/api/admin/abuse-policy", nil))
	policy := body["policy"].(map[string]any)
	assert.Equal(t, float64(abusepolicy.Default().LoginDistinctAccountsPerIP),
		policy["login_distinct_accounts_per_ip"])

	bounds := body["bounds"].([]any)
	require.NotEmpty(t, bounds)
	fields := map[string]bool{}
	for _, raw := range bounds {
		b := raw.(map[string]any)
		fields[b["field"].(string)] = true
		assert.Contains(t, b, "min")
		assert.Contains(t, b, "max")
		assert.Contains(t, b, "default")
	}
	assert.True(t, fields["login_distinct_accounts_per_ip"])
	assert.True(t, fields["public_click_coalesce_seconds"],
		"the nullable knob has to advertise its range like every other one")

	assert.Equal(t, true, body["can_write"], "the owner holds instance.rate_limits")
	assert.Contains(t, body, "observed")
}

func TestAbusePolicyAPI_PutStoresAndTakesEffectImmediately(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	res := owner.do(http.MethodPut, "/api/admin/abuse-policy", map[string]any{
		"login_distinct_accounts_per_ip": 7,
		"login_failures_per_account":     9,
		"login_window_minutes":           45,
		"api_writes_per_minute":          300,
		"api_expensive_per_hour":         50,
		"public_click_coalesce_seconds":  0,
		"anomaly_spray_accounts":         4,
		"anomaly_hammer_failures":        12,
		"anomaly_window_minutes":         60,
	})
	require.Equal(t, http.StatusOK, res.Code)
	saved := decode(t, res)["policy"].(map[string]any)
	assert.Equal(t, float64(7), saved["login_distinct_accounts_per_ip"])

	// It came back from the STORE, not from the request body.
	reread := decode(t, owner.do(http.MethodGet, "/api/admin/abuse-policy", nil))["policy"].(map[string]any)
	assert.Equal(t, float64(7), reread["login_distinct_accounts_per_ip"])
	assert.Equal(t, float64(0), reread["public_click_coalesce_seconds"],
		"0 is coalescing OFF and must survive as itself")

	// The trail records the edit: this is a change to the instance's rules.
	entries := decode(t, owner.do(http.MethodGet, "/api/admin/audit", nil))["entries"].([]any)
	seen := false
	for _, raw := range entries {
		e := raw.(map[string]any)
		if e["action"] == auth.AuditPolicyChanged {
			seen = true
		}
	}
	assert.True(t, seen, "an edit to the abuse limits must be in the trail")
}

// The message is returned VERBATIM because the form renders it. "Invalid" in
// front of a number field an owner just typed sends them guessing.
func TestAbusePolicyAPI_PutRefusesAnOutOfBoundsValueByName(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	res := owner.do(http.MethodPut, "/api/admin/abuse-policy", map[string]any{
		"login_distinct_accounts_per_ip": 1,
	})
	require.Equal(t, http.StatusBadRequest, res.Code)
	body := decode(t, res)["error"].(map[string]any)
	assert.Equal(t, "invalid_policy", body["code"])
	assert.Contains(t, body["message"], "login_distinct_accounts_per_ip")
	assert.Contains(t, body["message"], "3", "the real floor, so the owner can fix the form")
}

// The whole reason the permission is locked. An admin who could lower
// login_distinct_accounts_per_ip to its floor would lock the office out of the
// instance; one who could raise every ceiling would switch the defence off.
func TestAbusePolicyAPI_AnAdminReadsButNeverWrites(t *testing.T) {
	h := newHarnessWithGrants(t)
	h.bootstrapAdmin(t, "owner@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "admin@example.com", "a good password", "admin")
	admin := signIn(t, h, "admin@example.com", "a good password")

	body := decode(t, admin.do(http.MethodGet, "/api/admin/abuse-policy", nil))
	assert.Equal(t, false, body["can_write"],
		"the screen has to know to render the form disabled rather than offer a save that 403s")

	res := admin.do(http.MethodPut, "/api/admin/abuse-policy", map[string]any{
		"login_distinct_accounts_per_ip": 3,
	})
	assert.Equal(t, http.StatusForbidden, res.Code)
}

// INV-043: past /api/admin an ordinary account must not even learn the surface
// exists.
func TestAbusePolicyAPI_ANonAdminGets404(t *testing.T) {
	h := newHarnessWithGrants(t)
	h.bootstrapAdmin(t, "owner@example.com", "a good password")
	testdb.SeedUserWithPassword(t, h.pool, "editor@example.com", "a good password", "editor")
	editor := signIn(t, h, "editor@example.com", "a good password")

	assert.Equal(t, http.StatusNotFound,
		editor.do(http.MethodGet, "/api/admin/abuse-policy", nil).Code)
	assert.Equal(t, http.StatusNotFound,
		editor.do(http.MethodGet, "/api/admin/anomalies", nil).Code)
}

// ─────────────────────────────────────────────────────────────────────
// The detector
// ─────────────────────────────────────────────────────────────────────

// Spray is the rule the whole panel exists for: one origin walking a list of
// mailboxes. Depth against a single account is a DIFFERENT event and must not
// satisfy it.
func TestAnomalies_SprayCountsDistinctAccountsAndDepthAloneDoesNot(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	truncateTrail(t, h)

	for i := 0; i < 12; i++ {
		seedFailure(t, h, "203.0.113.7", fmt.Sprintf("victim-%d@example.com", i), time.Minute)
	}
	// One mailbox, past the hammer threshold: depth, never breadth.
	for i := 0; i < 25; i++ {
		seedFailure(t, h, "198.51.100.4", "one@example.com", time.Minute)
	}

	rows := anomalies(t, owner, "24h")
	byIPKind := map[string]map[string]any{}
	for _, raw := range rows {
		a := raw.(map[string]any)
		byIPKind[a["ip"].(string)+"/"+a["kind"].(string)] = a
	}
	spray, ok := byIPKind["203.0.113.7/spray"]
	require.True(t, ok, "twelve distinct accounts is a spray: %v", byIPKind)
	assert.Equal(t, float64(12), spray["distinct_accounts"])

	_, sprayed := byIPKind["198.51.100.4/spray"]
	assert.False(t, sprayed, "one mailbox is depth, not breadth — the office behind a NAT")
	hammer, ok := byIPKind["198.51.100.4/hammer"]
	require.True(t, ok, "twenty-five failures against one account is a hammer: %v", byIPKind)
	assert.Equal(t, float64(25), hammer["failures"])
	assert.Equal(t, float64(1), hammer["distinct_accounts"],
		"a hammer counts the accounts it crossed the threshold against, and there is one")
}

// One lockout is already the strongest signal the instance produces: the
// limiter did not merely observe the origin, it stopped answering it.
func TestAnomalies_ASingleThrottleIsEnoughAndIsCritical(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	truncateTrail(t, h)
	seedThrottle(t, h, "203.0.113.99", 2*time.Minute)

	rows := anomalies(t, owner, "24h")
	require.Len(t, rows, 1)
	a := rows[0].(map[string]any)
	assert.Equal(t, "throttle", a["kind"])
	assert.Equal(t, "203.0.113.99", a["ip"])
	assert.Equal(t, float64(1), a["throttles"])
	assert.Equal(t, "critical", a["severity"])
}

// The window is the whole meaning of the number. An event outside it is not a
// smaller anomaly, it is not one.
func TestAnomalies_TheWindowExcludesOlderEvents(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	truncateTrail(t, h)
	for i := 0; i < 12; i++ {
		seedFailure(t, h, "203.0.113.7", fmt.Sprintf("victim-%d@example.com", i), 3*time.Hour)
	}

	assert.Empty(t, anomalies(t, owner, "1h"), "three hours ago is outside a one-hour window")
	assert.NotEmpty(t, anomalies(t, owner, "24h"))
}

// The reply must never carry the mailbox that was attacked — only the count.
// The trail's own timeline already answers "which account", behind the read
// split INV-175 built; a second surface for it would be a second thing to
// guard.
func TestAnomalies_ThePayloadNamesNoAccount(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	truncateTrail(t, h)
	for i := 0; i < 12; i++ {
		seedFailure(t, h, "203.0.113.7", fmt.Sprintf("victim-%d@example.com", i), time.Minute)
	}
	for i := 0; i < 25; i++ {
		seedFailure(t, h, "198.51.100.4", "one@example.com", time.Minute)
	}

	res := owner.do(http.MethodGet, "/api/admin/anomalies?window=24h", nil)
	require.Equal(t, http.StatusOK, res.Code)
	raw := res.Body.String()
	assert.NotContains(t, raw, "@example.com",
		"an anomaly reports how many accounts, never which ones")
	assert.NotContains(t, strings.ToLower(raw), "victim")
}

// The provenance flag travels with the row (INV-176). Behind a proxy that is
// not in TRUSTED_PROXY_IPS every request arrives from the proxy's address and
// ip_trusted is false — the line is then about EVERYONE behind it, and the
// screen can only warn about that if the flag reaches it. Resolving it away
// here would recreate the address-without-provenance migration 000033 refused.
func TestAnomalies_CarriesTheProvenanceFlagOfTheOrigin(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	truncateTrail(t, h)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		_, err := h.pool.Exec(ctx, `
			INSERT INTO audit_log (action, target_email, ip, ip_trusted, created_at)
			VALUES ($1, $2, '203.0.113.7'::inet, true, now())`,
			auth.AuditLoginFailed, fmt.Sprintf("victim-%d@example.com", i))
		require.NoError(t, err)
	}

	rows := anomalies(t, owner, "24h")
	require.NotEmpty(t, rows)
	assert.Equal(t, true, rows[0].(map[string]any)["ip_trusted"])
}

// A blocked origin still appears, flagged — removing it would make the panel
// forget why the block is there the moment it works.
func TestAnomalies_ReportsWhetherTheOriginIsAlreadyBlocked(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	truncateTrail(t, h)
	for i := 0; i < 12; i++ {
		seedFailure(t, h, "203.0.113.7", fmt.Sprintf("victim-%d@example.com", i), time.Minute)
	}
	require.Equal(t, http.StatusCreated, owner.do(http.MethodPost, "/api/admin/audit/blocks",
		map[string]string{"ip": "203.0.113.7", "reason": "spray"}).Code)

	rows := anomalies(t, owner, "24h")
	require.NotEmpty(t, rows)
	assert.Equal(t, true, rows[0].(map[string]any)["blocked"])
}

// The detector REPORTS. It never installs a block, because a heuristic holding
// that button locks the operator out at three in the morning (INV-178).
func TestAnomalies_DetectingNeverBlocks(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	truncateTrail(t, h)
	for i := 0; i < 40; i++ {
		seedFailure(t, h, "203.0.113.7", fmt.Sprintf("victim-%d@example.com", i), time.Minute)
	}
	seedThrottle(t, h, "203.0.113.7", time.Minute)

	require.NotEmpty(t, anomalies(t, owner, "24h"))
	blocks := decode(t, owner.do(http.MethodGet, "/api/admin/audit/blocks", nil))["blocks"]
	assert.Empty(t, blocks, "the panel ranks and reports; blocking stays a human act")
}

// The thresholds the panel applied travel with the answer, so the screen can
// say WHY a row is on it instead of restating numbers it copied.
func TestAnomalies_EchoesTheWindowAndTheThresholdsItApplied(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	require.Equal(t, http.StatusOK, owner.do(http.MethodPut, "/api/admin/abuse-policy",
		map[string]any{"anomaly_spray_accounts": 3, "anomaly_hammer_failures": 4,
			"anomaly_window_minutes": 60}).Code)

	body := decode(t, owner.do(http.MethodGet, "/api/admin/anomalies", nil))
	assert.Equal(t, "60m", body["window"], "no window named means the configured one")
	th := body["thresholds"].(map[string]any)
	assert.Equal(t, float64(3), th["spray_accounts"])
	assert.Equal(t, float64(4), th["hammer_failures"])
	assert.Equal(t, float64(60), th["window_minutes"])
}

func TestAnomalies_RefusesAWindowOutsideTheVocabulary(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")
	res := owner.do(http.MethodGet, "/api/admin/anomalies?window=90d", nil)
	require.Equal(t, http.StatusBadRequest, res.Code)
	assert.Equal(t, "invalid_window", decode(t, res)["error"].(map[string]any)["code"])
}

// anomalies runs the endpoint and returns its rows.
func anomalies(t *testing.T, c *client, window string) []any {
	t.Helper()
	res := c.do(http.MethodGet, "/api/admin/anomalies?window="+window, nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var body struct {
		Anomalies []any `json:"anomalies"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	return body.Anomalies
}

// The migration is verified against the DATABASE, not against its exit code.
// CLAUDE.md §7 records what trusting the exit code cost once: a migration file
// and a running schema disagreed, nothing reported it, and the disagreement
// surfaced later as a query against an object that existed in the repo and not
// in the instance.
func TestMigration_AuditIPIndexExists(t *testing.T) {
	h := newHarnessWithGrants(t)
	var def string
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes
		 WHERE tablename = 'audit_log' AND indexname = 'audit_log_ip_time_idx'`).Scan(&def))
	assert.Contains(t, def, "ip")
	assert.Contains(t, def, "created_at DESC")
	assert.Contains(t, def, "WHERE (ip IS NOT NULL)",
		"the partial predicate is what keeps the index off the bulk of the table")
}

// A degraded database must answer 500, never a plausible-looking policy.
//
// These are the arms that decide what the instance ENFORCES and what the
// screen calls an anomaly. A read failure answered with the defaults would show
// an owner limits they did not set, and an empty anomaly list would read as
// "no attack" during the one moment that answer matters.
//
// Called through the handler rather than the router on purpose: with the pool
// closed, Authenticate fails first and every request is a 401, so the router
// can never reach these branches. authctxtest supplies the principal the gate
// would have.
func TestAbuseAPI_ADatabaseFailureIsAnErrorNotAPlausibleAnswer(t *testing.T) {
	pool := testdb.Shared(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := auth.NewAbuseHandler(auth.NewRepository(pool), abusepolicy.NewRepository(pool),
		nil, logger, nil, roleperm.Default())
	pool.Close() // every subsequent query fails

	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"get":       h.GetPolicy,
		"put":       h.PutPolicy,
		"anomalies": h.Anomalies,
	} {
		rec := httptest.NewRecorder()
		req := authctxtest.Request(
			httptest.NewRequest(http.MethodGet, "/x", strings.NewReader(`{}`)),
			authctxtest.DefaultUser)
		call(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code, "%s", name)
	}
}

// A body naming a knob that does not exist is a 400, not a save that silently
// drops it: a form field renamed on one side only would otherwise look saved.
func TestAbusePolicyAPI_RefusesAnUnknownField(t *testing.T) {
	h := newHarnessWithGrants(t)
	owner := h.bootstrapAdmin(t, "owner@example.com", "a good password")

	res := owner.do(http.MethodPut, "/api/admin/abuse-policy",
		map[string]any{"login_distinct_accounts_per_ips": 7})
	require.Equal(t, http.StatusBadRequest, res.Code)
	assert.Equal(t, "invalid_json", decode(t, res)["error"].(map[string]any)["code"])
}

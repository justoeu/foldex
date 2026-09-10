//go:build integration

package backupstatus_test

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

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backupstatus"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/roleperm"
	"foldex/internal/testdb"
)

const goodPassword = "a-long-enough-passphrase"

// fakeAgent stands in for the backup agent's /internal/artifact endpoint. It
// records what the backend sent — which is how the tests below prove the
// passphrase travels in the BODY and reaches the agent unaltered.
type fakeAgent struct {
	server   *httptest.Server
	gotKey   string
	gotPass  string
	body     string
	status   int
	requests int
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	f := &fakeAgent{body: "age-encrypted-bytes", status: http.StatusOK}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		var in struct {
			Key        string `json:"key"`
			Passphrase string `json:"passphrase"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.gotKey, f.gotPass = in.Key, in.Passphrase
		if f.status != http.StatusOK {
			w.WriteHeader(f.status)
			return
		}
		_, _ = io.WriteString(w, f.body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// downloadHarness mounts the handler the way the server does — RequireAdmin
// first, then the permission gate — so these tests exercise the 404-for-
// non-admin contract (INV-043) rather than only the handler body.
type downloadHarness struct {
	t       *testing.T
	pool    *pgxpool.Pool
	router  http.Handler
	audited []string
	runID   int64
	// Two real accounts: backup_download references app_user, so a principal
	// whose id is not a row cannot reserve anything.
	owner  authctx.UserID
	adminA authctx.UserID
	adminB authctx.UserID
}

// newDownloadHarness wires the real router. `agent` nil means the bridge is
// UNCONFIGURED, which is the default posture an operator who never set
// BACKUP_AGENT_* keeps (INV-187).
func newDownloadHarness(t *testing.T, agent *fakeAgent) *downloadHarness {
	t.Helper()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))

	h := &downloadHarness{t: t, pool: pool}
	var client *backupstatus.ArtifactClient
	if agent != nil {
		client = backupstatus.NewArtifactClient(agent.server.URL, "t0ken")
	}
	handler := backupstatus.NewHandler(
		backupstatus.NewRepository(pool),
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		nil, nil,
		func(_ *http.Request, detail string) { h.audited = append(h.audited, detail) },
		roleperm.OrDefault(nil),
		client,
	)
	r := chi.NewRouter()
	r.Route("/api/admin", func(ar chi.Router) {
		ar.Use(authgate.RequireAdmin)
		ar.Route("/backup", handler.Mount)
	})
	h.router = r

	h.owner = testdb.SeedUser(t, pool, "owner@example.com", "owner")
	h.adminA = testdb.SeedUser(t, pool, "a@example.com", "admin")
	h.adminB = testdb.SeedUser(t, pool, "b@example.com", "admin")
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at, artifact_key)
		VALUES ('dump', 'succeeded', now(), now(), now(), $1) RETURNING id`,
		artifactKey).Scan(&h.runID))
	return h
}

func (h *downloadHarness) do(role authctx.Role, uid authctx.UserID, method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{
		UserID: uid, Role: role, SessionID: 1, Via: "session",
	}))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

/*
The owner, because a whole-instance artifact is owner-only (INV-187): these

	tests exercise the download PIPELINE, and who may reach it is the subject of
	the ownership tests further down.
*/
func (h *downloadHarness) download(password string) *httptest.ResponseRecorder {
	return h.do(authctx.RoleOwner, h.owner, http.MethodPost,
		fmt.Sprintf("/api/admin/backup/runs/%d/download", h.runID),
		fmt.Sprintf(`{"password":%q}`, password))
}

/*
 * The happy path, end to end through the real router: the passphrase reaches
 * the agent in the BODY, the bytes reach the caller, the trail records it, and
 * the reservation is spent.
 */
func TestDownload_StreamsTheArtifactAndRecordsIt(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)

	rec := h.download(goodPassword)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "age-encrypted-bytes", rec.Body.String())
	assert.Equal(t, artifactKey, agent.gotKey)
	assert.Equal(t, goodPassword, agent.gotPass, "the passphrase must reach the agent unaltered")

	// Named so a browser saves something openable, and always .age — a file
	// called .dump that age wrote fails in pg_restore looking like corruption.
	assert.Contains(t, rec.Header().Get("Content-Disposition"), `filename="foldex.dump.age"`)

	assert.Equal(t, []string{artifactKey}, h.audited,
		"an artifact leaving the instance is not a routine event")

	var used int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM backup_download WHERE run_id = $1`, h.runID).Scan(&used))
	assert.Equal(t, 1, used)
}

// The passphrase must not come back out anywhere the caller can read it.
func TestDownload_TheResponseNeverEchoesThePassphrase(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)

	rec := h.download(goodPassword)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), goodPassword)
	for name, values := range rec.Header() {
		for _, v := range values {
			assert.NotContains(t, v, goodPassword, "header %q", name)
		}
	}

	// And there is no column it could have landed in.
	var cols int
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'backup_download'
		  AND column_name ~* 'pass|secret|token|key' AND column_name <> 'object_key'`).Scan(&cols))
	assert.Zero(t, cols, "the passphrase must have nowhere to be persisted")
}

func TestDownload_RefusesOnceTheBudgetIsSpent(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)

	for i := 0; i < backupstatus.MaxDownloadsPerAdmin; i++ {
		require.Equal(t, http.StatusOK, h.download(goodPassword).Code, "download %d", i+1)
	}
	rec := h.download(goodPassword)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "download_budget_spent", errCode(t, rec))
	assert.Equal(t, backupstatus.MaxDownloadsPerAdmin, agent.requests,
		"a refused download must not reach the agent at all")
}

// Another administrator has their own three — the ceiling is per account.
func TestDownload_ASecondAdministratorHasTheirOwnBudget(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)
	path := fmt.Sprintf("/api/admin/backup/runs/%d/download", h.runID)
	body := fmt.Sprintf(`{"password":%q}`, goodPassword)

	// Two OWNER-role principals: the budget is per account, and this test is
	// about the account, not the role.
	for i := 0; i < backupstatus.MaxDownloadsPerAdmin; i++ {
		require.Equal(t, http.StatusOK, h.do(authctx.RoleOwner, h.owner, http.MethodPost, path, body).Code)
	}
	require.Equal(t, http.StatusTooManyRequests, h.do(authctx.RoleOwner, h.owner, http.MethodPost, path, body).Code)
	assert.Equal(t, http.StatusOK, h.do(authctx.RoleOwner, h.adminB, http.MethodPost, path, body).Code)
}

/*
 * A bridge that never answered moved no bytes, so it costs nothing.
 *
 * The budget counts what LEFT the instance. Charging for an unreachable agent
 * would mean three failed attempts lock the owner out of that artifact
 * permanently — while they are in the middle of fixing the very bridge that
 * failed. This is the one refund; a stream that starts and dies keeps its
 * charge (TestCompleteDownload_RecordsTheOutcomeWithoutRefundingTheReservation).
 */
func TestDownload_AFailedBridgeCostsNothing(t *testing.T) {
	agent := newFakeAgent(t)
	agent.status = http.StatusBadGateway
	h := newDownloadHarness(t, agent)

	for range backupstatus.MaxDownloadsPerAdmin + 1 {
		rec := h.download(goodPassword)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Equal(t, "artifact_unavailable", errCode(t, rec))
	}

	budget := decode(t, h.do(authctx.RoleOwner, h.owner, http.MethodGet,
		fmt.Sprintf("/api/admin/backup/runs/%d/download-budget", h.runID), ""))
	assert.EqualValues(t, 0, budget["used"], "a bridge that served nothing spent nothing")
	assert.EqualValues(t, backupstatus.MaxDownloadsPerAdmin, budget["available"])

	// And the artifact is still reachable once the bridge is fixed — the point
	// of the refund.
	agent.status = http.StatusOK
	assert.Equal(t, http.StatusOK, h.download(goodPassword).Code)
}

// Unconfigured is the DEFAULT, and it must look like a feature the instance
// does not have rather than one that is temporarily down (INV-043's reasoning).
func TestDownload_WithoutTheBridgeTheRouteDoesNotExist(t *testing.T) {
	h := newDownloadHarness(t, nil)

	assert.Equal(t, http.StatusNotFound, h.download(goodPassword).Code)
	assert.Equal(t, http.StatusNotFound, h.do(authctx.RoleOwner, h.owner, http.MethodGet,
		fmt.Sprintf("/api/admin/backup/runs/%d/download-budget", h.runID), "").Code)
	assert.Equal(t, http.StatusNotFound, h.do(authctx.RoleOwner, h.owner, http.MethodGet,
		"/api/admin/backup/user-zips", "").Code)
}

// A non-admin gets 404, never 403: the admin surface does not confirm it exists.
func TestDownload_ANonAdminIsToldNothing(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)

	for _, role := range []authctx.Role{authctx.RoleEditor, authctx.RoleViewer} {
		rec := h.do(role, h.adminA, http.MethodPost,
			fmt.Sprintf("/api/admin/backup/runs/%d/download", h.runID),
			fmt.Sprintf(`{"password":%q}`, goodPassword))
		assert.Equal(t, http.StatusNotFound, rec.Code, "role %q", role)
	}
	assert.Zero(t, agent.requests)
}

// A mirror or user_zip run shipped N objects and names none of them. Saying so
// beats a 404 that would read as "this run does not exist" for a run the
// operator is looking straight at.
func TestDownload_ARunWithNoSingleArtifactSaysSo(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)
	var mirrorID int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at)
		VALUES ('mirror', 'succeeded', now(), now(), now()) RETURNING id`).Scan(&mirrorID))

	rec := h.do(authctx.RoleOwner, h.owner, http.MethodPost,
		fmt.Sprintf("/api/admin/backup/runs/%d/download", mirrorID),
		fmt.Sprintf(`{"password":%q}`, goodPassword))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "no_artifact", errCode(t, rec))
	assert.Zero(t, agent.requests)
}

func TestDownload_RefusesAPasswordBelowTheFloorWithoutSpendingAnything(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)

	rec := h.download("short")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "password_too_short", errCode(t, rec))
	assert.Zero(t, agent.requests, "a refused password must not reach the agent")

	budget := decode(t, h.do(authctx.RoleOwner, h.owner, http.MethodGet,
		fmt.Sprintf("/api/admin/backup/runs/%d/download-budget", h.runID), ""))
	assert.EqualValues(t, 0, budget["used"], "a local refusal must not cost a download")
}

func TestDownload_AnUnknownRunIsNotFound(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)

	rec := h.do(authctx.RoleOwner, h.owner, http.MethodPost,
		fmt.Sprintf("/api/admin/backup/runs/%d/download", h.runID+9999),
		fmt.Sprintf(`{"password":%q}`, goodPassword))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Zero(t, agent.requests)
}

func TestDownloadBudget_ReportsWhatIsLeft(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)
	path := fmt.Sprintf("/api/admin/backup/runs/%d/download-budget", h.runID)

	budget := decode(t, h.do(authctx.RoleOwner, h.owner, http.MethodGet, path, ""))
	assert.EqualValues(t, 0, budget["used"])
	assert.EqualValues(t, backupstatus.MaxDownloadsPerAdmin, budget["available"])

	require.Equal(t, http.StatusOK, h.download(goodPassword).Code)
	budget = decode(t, h.do(authctx.RoleOwner, h.owner, http.MethodGet, path, ""))
	assert.EqualValues(t, 1, budget["used"])
	assert.EqualValues(t, backupstatus.MaxDownloadsPerAdmin-1, budget["available"])
}

/*
 * The line §0 draws: an administrator takes NOTHING from this route.
 *
 * A dump is indivisible — pg_dump has no per-account slice — so "download only
 * what you own" for a whole-instance artifact means "only the owner". And the
 * per-user ZIPs are not carved out for administrators either, because that
 * would be a second door into a room that already has one: /api/backup already
 * exports any account's own data, so a second path to the same bytes is
 * surface for no capability.
 *
 * The refusal comes from the permission gate (403), before the handler runs —
 * the ownership rule inside it is the second layer, tested directly in
 * download_ownership_test.go.
 */
func TestDownload_AnAdministratorTakesNothingFromThisRoute(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)

	own := fmt.Sprintf("backups/users/%d/20260909-000000.zip.age", int64(h.adminA))
	var ownRun int64
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at, artifact_key)
		VALUES ('user_zip', 'succeeded', now(), now(), now(), $1) RETURNING id`, own).Scan(&ownRun))

	for _, runID := range []int64{h.runID, ownRun} {
		rec := h.do(authctx.RoleAdmin, h.adminA, http.MethodPost,
			fmt.Sprintf("/api/admin/backup/runs/%d/download", runID),
			fmt.Sprintf(`{"password":%q}`, goodPassword))
		assert.Equal(t, http.StatusForbidden, rec.Code, "run %d", runID)
		assert.Equal(t, "forbidden_role", errCode(t, rec))
	}
	assert.Zero(t, agent.requests, "a refused download must never reach the agent")

	var spent int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM backup_download`).Scan(&spent))
	assert.Zero(t, spent, "a refusal must not cost a download either")
}

func TestDownload_TheOwnerMayTakeTheWholeInstanceArtifact(t *testing.T) {
	agent := newFakeAgent(t)
	h := newDownloadHarness(t, agent)

	rec := h.do(authctx.RoleOwner, h.owner, http.MethodPost,
		fmt.Sprintf("/api/admin/backup/runs/%d/download", h.runID),
		fmt.Sprintf(`{"password":%q}`, goodPassword))
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

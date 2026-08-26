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
	"os"
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

// TestMain stops the shared container this package's tests start. Only here is
// late enough — a t.Cleanup on whichever test ran first would kill the
// container while the rest of the package still needed it (CLAUDE.md §2).
func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}

// harness mounts the handler the way the server does — RequireAdmin first,
// then the permission gate — so the tests exercise the 404-for-non-admin
// contract (INV-043) and not just the handler body.
type harness struct {
	t      *testing.T
	pool   *pgxpool.Pool
	router http.Handler
}

func newHarness(t *testing.T, grants authgate.Grants) *harness {
	t.Helper()
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))

	h := backupstatus.NewHandler(
		backupstatus.NewRepository(pool),
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		nil,
		grants,
	)
	r := chi.NewRouter()
	r.Route("/api/admin", func(ar chi.Router) {
		ar.Use(authgate.RequireAdmin)
		ar.Route("/backup", h.Mount)
	})
	return &harness{t: t, pool: pool, router: r}
}

func (h *harness) do(role authctx.Role, method, path string, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{
		UserID: 1, Role: role, SessionID: 1, Via: "session",
	}))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), "body: %s", rec.Body.String())
	return out
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := decode(t, rec)
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// seedRun inserts one finished backup_run row directly — the agent is the real
// writer, and these tests only need its footprints.
func (h *harness) seedRun(job, status string, artifactKey string, lastError string, meta string) int64 {
	h.t.Helper()
	var key, lerr *string
	if artifactKey != "" {
		key = &artifactKey
	}
	if lastError != "" {
		lerr = &lastError
	}
	if meta == "" {
		meta = "{}"
	}
	var id int64
	err := h.pool.QueryRow(context.Background(), `
		INSERT INTO backup_run (job, status, scheduled_for, started_at, finished_at,
			artifact_key, artifact_bytes, artifact_sha256, last_error, meta)
		VALUES ($1, $2, now(), now(), now(), $3,
			CASE WHEN $3::text IS NULL THEN NULL ELSE 12345 END,
			CASE WHEN $3::text IS NULL THEN NULL ELSE 'abcd1234' END,
			$4, $5::jsonb)
		RETURNING id`, job, status, key, lerr, meta).Scan(&id)
	require.NoError(h.t, err)
	return id
}

// A non-admin must get the 404 a route that does not exist would give, never a
// 403 that confirms the surface is there (INV-043).
func TestBackupStatus_NonAdminGetsNotFound(t *testing.T) {
	h := newHarness(t, roleperm.Default())
	for _, role := range []authctx.Role{authctx.RoleEditor, authctx.RoleViewer} {
		rec := h.do(role, http.MethodGet, "/api/admin/backup/runs", "")
		assert.Equal(t, http.StatusNotFound, rec.Code, "role %s", role)
		assert.Equal(t, "not_found", errCode(t, rec), "role %s", role)

		rec = h.do(role, http.MethodPost, "/api/admin/backup/run", `{"job":"dump"}`)
		assert.Equal(t, http.StatusNotFound, rec.Code, "role %s", role)
	}
}

// An administrator whose role had instance.backup unticked gets a 403: past
// the admin gate the surface's existence is no secret, and the finer refusal
// tells them something true about their own grant.
func TestBackupStatus_AdminWithoutTheGrantGetsForbidden(t *testing.T) {
	stripped := roleperm.Resolve(map[authctx.Role][]authctx.Permission{
		authctx.RoleAdmin: {authctx.PermUsersRead},
	})
	h := newHarness(t, stripped)

	rec := h.do(authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/runs", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "forbidden_role", errCode(t, rec))

	// The owner reads the compiled matrix whatever the store says.
	rec = h.do(authctx.RoleOwner, http.MethodGet, "/api/admin/backup/runs", "")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestBackupStatus_SummaryReflectsSeededRuns(t *testing.T) {
	h := newHarness(t, roleperm.Default())

	dumpID := h.seedRun("dump", "succeeded", "foldex/dump/2026-08-26.dump.age", "", `{"tables":{"link":10}}`)
	// A real failure after the success counts; operational reasons do not.
	h.seedRun("dump", "failed", "", "upload_failed", "")
	h.seedRun("dump", "failed", "", "shutdown", "")
	// The drill validated the dump above.
	drillID := h.seedRun("drill", "succeeded", "", "", `{"tables":{"link":10},"schema_version":41}`)
	_, err := h.pool.Exec(context.Background(),
		`UPDATE backup_run SET drill_of_run_id = $1 WHERE id = $2`, dumpID, drillID)
	require.NoError(t, err)

	rec := h.do(authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/runs", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)

	jobs, ok := body["jobs"].([]any)
	require.True(t, ok)
	require.Len(t, jobs, 4, "one summary entry per job, always — absent jobs render as never-ran")

	byJob := map[string]map[string]any{}
	for _, j := range jobs {
		entry := j.(map[string]any)
		byJob[entry["job"].(string)] = entry
	}

	dump := byJob["dump"]
	require.NotNil(t, dump["last_success"], "the seeded success must surface")
	last := dump["last_success"].(map[string]any)
	assert.Equal(t, "foldex/dump/2026-08-26.dump.age", last["artifact_key"])
	assert.EqualValues(t, 12345, last["artifact_bytes"])
	assert.Equal(t, "abcd1234", last["artifact_sha256"])
	assert.EqualValues(t, 1, dump["consecutive_failures"],
		"upload_failed counts, shutdown is an operational outcome and must not")

	drill := byJob["drill"]
	require.NotNil(t, drill["last_success"])
	drillLast := drill["last_success"].(map[string]any)
	assert.EqualValues(t, dumpID, drillLast["drill_of_run_id"],
		"the drill highlight must say WHICH dump it proved")
	meta := drillLast["meta"].(map[string]any)
	assert.EqualValues(t, 41, meta["schema_version"])

	for _, never := range []string{"mirror", "user_zip"} {
		assert.Nil(t, byJob[never]["last_success"], "job %s never ran", never)
		assert.EqualValues(t, 0, byJob[never]["consecutive_failures"])
	}

	runs := body["runs"].([]any)
	assert.Len(t, runs, 4, "the first history page carries every seeded row")
	// Newest first: the failed rows landed after the dump success.
	first := runs[0].(map[string]any)
	assert.EqualValues(t, drillID, first["id"])
}

func TestBackupStatus_ListFiltersClampsAndPaginates(t *testing.T) {
	h := newHarness(t, roleperm.Default())
	for i := 0; i < 25; i++ {
		h.seedRun("mirror", "succeeded", "", "", "")
	}
	h.seedRun("dump", "succeeded", "k", "", "")

	// Default limit is 20.
	body := decode(t, h.do(authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/runs", ""))
	assert.Len(t, body["runs"].([]any), 20)

	// job filter composes with the page.
	body = decode(t, h.do(authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/runs?job=dump", ""))
	require.Len(t, body["runs"].([]any), 1)

	// An unparseable limit falls back to the default; an oversized one clamps
	// to 100 (observable as "everything", since 26 < 100).
	body = decode(t, h.do(authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/runs?limit=abc", ""))
	assert.Len(t, body["runs"].([]any), 20)
	body = decode(t, h.do(authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/runs?limit=99999", ""))
	assert.Len(t, body["runs"].([]any), 26)

	// Keyset cursor: the page after the newest two starts below their ids.
	body = decode(t, h.do(authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/runs?limit=2", ""))
	runs := body["runs"].([]any)
	require.Len(t, runs, 2)
	lastID := int64(runs[1].(map[string]any)["id"].(float64))
	body = decode(t, h.do(authctx.RoleAdmin, http.MethodGet,
		fmt.Sprintf("/api/admin/backup/runs?limit=2&before=%d", lastID), ""))
	next := body["runs"].([]any)
	require.Len(t, next, 2)
	assert.Less(t, next[0].(map[string]any)["id"].(float64), float64(lastID))

	rec := h.do(authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/runs?job=nonsense", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_job", errCode(t, rec))

	rec = h.do(authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/runs?before=notanid", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_cursor", errCode(t, rec))
}

func TestBackupStatus_RequestRunEnqueuesOnceAndConflictsWhilePending(t *testing.T) {
	h := newHarness(t, roleperm.Default())

	rec := h.do(authctx.RoleOwner, http.MethodPost, "/api/admin/backup/run", `{"job":"dump"}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Equal(t, "requested", body["status"])
	assert.Equal(t, "dump", body["job"])

	// A second click while the first is unclaimed must not queue.
	rec = h.do(authctx.RoleOwner, http.MethodPost, "/api/admin/backup/run", `{"job":"dump"}`)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "backup_run_pending", errCode(t, rec))

	// A different job is independent.
	rec = h.do(authctx.RoleOwner, http.MethodPost, "/api/admin/backup/run", `{"job":"mirror"}`)
	assert.Equal(t, http.StatusAccepted, rec.Code)

	// A running row blocks exactly like a requested one.
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO backup_run (job, status, scheduled_for) VALUES ('drill', 'running', now())`)
	require.NoError(t, err)
	rec = h.do(authctx.RoleOwner, http.MethodPost, "/api/admin/backup/run", `{"job":"drill"}`)
	assert.Equal(t, http.StatusConflict, rec.Code)

	// Finished rows never block a new request.
	require.NoError(t, testdb.Reset(context.Background(), h.pool))
	h.seedRun("dump", "failed", "", "upload_failed", "")
	rec = h.do(authctx.RoleOwner, http.MethodPost, "/api/admin/backup/run", `{"job":"dump"}`)
	assert.Equal(t, http.StatusAccepted, rec.Code)

	rec = h.do(authctx.RoleOwner, http.MethodPost, "/api/admin/backup/run", `{"job":"nonsense"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_job", errCode(t, rec))
}

func TestRequestRun_FiresTheAuditHookWithTheJob(t *testing.T) {
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))

	var audited []string
	h := backupstatus.NewHandler(
		backupstatus.NewRepository(pool),
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		func(_ *http.Request, job string) { audited = append(audited, job) },
		roleperm.Default(),
	)
	r := chi.NewRouter()
	r.Route("/api/admin", func(ar chi.Router) {
		ar.Use(authgate.RequireAdmin)
		ar.Route("/backup", h.Mount)
	})
	hr := &harness{t: t, pool: pool, router: r}
	rec := hr.do(authctx.RoleOwner, http.MethodPost, "/api/admin/backup/run", `{"job":"dump"}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	// The row IS the record for the agent, but the trail is what the ADMIN
	// surface answers with — a silently dropped hook survives every other
	// test because the shared harness passes nil.
	assert.Equal(t, []string{"dump"}, audited)
}

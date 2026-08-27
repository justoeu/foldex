//go:build integration

package backupstatus_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backupagent"
	"foldex/internal/backupstatus"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/roleperm"
	"foldex/internal/testdb"
)

// seedUser inserts an account and returns its id — PutSchedule records the
// caller in updated_by, and the column's FK needs a real row behind it.
func (h *harness) seedUser(email string, role string) int64 {
	h.t.Helper()
	var id int64
	require.NoError(h.t, h.pool.QueryRow(context.Background(), `
		INSERT INTO app_user (email, email_normalized, name, role, status, password_hash)
		VALUES ($1, $1, 'Someone', $2, 'active', '$2a$10$test.hash.not.a.real.credential.abcdefghijk')
		RETURNING id`, email, role).Scan(&id))
	return id
}

// doAs is harness.do with a caller identity — the schedule surface writes the
// acting user into updated_by, so the fixed UserID 1 is not enough here.
func (h *harness) doAs(userID int64, role authctx.Role, method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{
		UserID: authctx.UserID(userID), Role: role, SessionID: 1, Via: "session",
	}))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func TestSchedule_PutStoresRowAndGetServesIt(t *testing.T) {
	h := newHarness(t, roleperm.Default())
	uid := h.seedUser("owner@foldex.test", "owner")

	rec := h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/dump",
		`{"mode":"times","times":["06:00","18:00"],"weekdays":["mon","tue","wed","thu","fri"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = h.doAs(uid, authctx.RoleOwner, http.MethodGet, "/api/admin/backup/schedule", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body := decode(t, rec)

	rows, _ := body["rows"].(map[string]any)
	require.Contains(t, rows, "dump")
	dump, _ := rows["dump"].(map[string]any)
	cfg, _ := dump["config"].(map[string]any)
	assert.Equal(t, "times", cfg["mode"])
	assert.Equal(t, []any{"06:00", "18:00"}, cfg["times"])
	assert.Equal(t, []any{"mon", "tue", "wed", "thu", "fri"}, cfg["weekdays"])
	assert.Equal(t, "owner@foldex.test", dump["updated_by_email"],
		"the band says who moved the agenda")

	// One vocabulary for every job, so one set of bounds: the form refuses
	// locally what the server would refuse anyway.
	bounds, _ := body["bounds"].(map[string]any)
	assert.Equal(t, float64(backupagent.MinTimes), bounds["times_min"])
	assert.Equal(t, float64(backupagent.MaxTimes), bounds["times_max"])
	assert.Equal(t, float64(backupagent.MinWeekdays), bounds["weekdays_min"])
	assert.Equal(t, float64(backupagent.MinDumpWeekdays), bounds["dump_weekdays_min"])
	assert.Equal(t, float64(backupagent.MinIntervalMin), bounds["interval_min"])
	assert.Equal(t, float64(backupagent.MaxIntervalMin), bounds["interval_max"])

	assert.Nil(t, body["agent"],
		"no heartbeat ever written must serve null — a zero time would render as 1970 and read as a bug")
}

func TestSchedule_FloorsAnswer400WithTheReason(t *testing.T) {
	h := newHarness(t, roleperm.Default())
	uid := h.seedUser("owner@foldex.test", "owner")

	rec := h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/dump",
		`{"mode":"times","times":[],"weekdays":["mon","tue","wed","thu","fri"]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_schedule", errCode(t, rec))
	assert.Contains(t, rec.Body.String(), "never zero",
		"the refusal names the floor — a bare invalid sends the owner guessing (INV-169's reasoning)")

	// The dump's weekday floor is higher than every other job's, and the
	// refusal has to say the number: the form renders this message as it is.
	rec = h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/dump",
		`{"mode":"times","times":["06:00"],"weekdays":["mon","wed","fri"]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_schedule", errCode(t, rec))
	assert.Contains(t, rec.Body.String(), "5")

	// Three weekdays are fine for every other job — the dump is the outlier
	// because it is the instance's disaster floor.
	rec = h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/drill",
		`{"mode":"times","times":["01:00"],"weekdays":["mon","wed","fri"]}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/mirror",
		`{"mode":"interval","interval_min":5}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_schedule", errCode(t, rec))

	// A client that did not upgrade: the legacy vocabulary is read-only.
	rec = h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/drill",
		`{"time":"01:00","weekday":"sun"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_schedule", errCode(t, rec))

	rec = h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/dump",
		`{"times":["06:00"],"weekdays":["mon","tue","wed","thu","fri"]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"the mode is explicit — a document without one would be half-honoured in silence")

	rec = h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/vacuum",
		`{"interval_min":60}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_job", errCode(t, rec))
}

func TestSchedule_WriteIsOwnerOnlyThroughTheLockedPermission(t *testing.T) {
	h := newHarness(t, roleperm.Default())
	admin := h.seedUser("admin@foldex.test", "admin")

	// An admin reads the agenda (instance.backup) but cannot move it:
	// instance.backup_schedule is locked owner-only — an administrator who
	// could stretch the dump schedule could thin the instance's DR.
	rec := h.doAs(admin, authctx.RoleAdmin, http.MethodGet, "/api/admin/backup/schedule", "")
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = h.doAs(admin, authctx.RoleAdmin, http.MethodPut, "/api/admin/backup/schedule/dump",
		`{"mode":"times","times":["06:00"],"weekdays":["mon","tue","wed","thu","fri"]}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = h.doAs(admin, authctx.RoleAdmin, http.MethodDelete, "/api/admin/backup/schedule/dump", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSchedule_DeleteResetsToTheEnvBaselineAndAudits(t *testing.T) {
	pool := testdb.Shared(t)
	require.NoError(t, testdb.Reset(context.Background(), pool))

	var audited []string
	handler := backupstatus.NewHandler(
		backupstatus.NewRepository(pool),
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		nil,
		func(_ *http.Request, detail string) { audited = append(audited, detail) },
		roleperm.Default(),
	)
	r := chi.NewRouter()
	r.Route("/api/admin", func(ar chi.Router) {
		ar.Use(authgate.RequireAdmin)
		ar.Route("/backup", handler.Mount)
	})
	h := &harness{t: t, pool: pool, router: r}
	uid := h.seedUser("owner@foldex.test", "owner")

	rec := h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/user_zip",
		`{"mode":"times","times":["02:30"],"weekdays":["mon","tue","wed","thu","fri"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// A second PUT over the same job: the trail is the only place that can
	// still say what the agenda was before this request replaced it.
	rec = h.doAs(uid, authctx.RoleOwner, http.MethodPut, "/api/admin/backup/schedule/user_zip",
		`{"mode":"times","enabled":false}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = h.doAs(uid, authctx.RoleOwner, http.MethodDelete, "/api/admin/backup/schedule/user_zip", "")
	assert.Equal(t, http.StatusNoContent, rec.Code)

	rows, err := backupagent.NewScheduleStore(pool).Load(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rows)

	require.Len(t, audited, 3, "every edit and the reset land in the trail")
	assert.Contains(t, audited[0], "user_zip schedule set")
	assert.Contains(t, audited[0], "env baseline",
		"the first edit says the job was on the env agenda, because it was")
	assert.Contains(t, audited[0], `"times":["02:30"]`)

	// One PUT now moves the mode, the weekday set and every time at once, so
	// the trail carries the whole document on both sides (INV-047).
	assert.Contains(t, audited[1], `"times":["02:30"]`,
		"what the agenda WAS is the half a bare \"user_zip schedule set\" could never answer")
	assert.Contains(t, audited[1], `"weekdays":["mon","tue","wed","thu","fri"]`)
	assert.Contains(t, audited[1], `"enabled":false`)

	assert.Contains(t, audited[2], "user_zip schedule reset to the env baseline")
	assert.Contains(t, audited[2], `"enabled":false`,
		"a delete's whole content is what was reset AWAY — the row is gone from backup_schedule")
}

func TestSchedule_GetServesTheHeartbeat(t *testing.T) {
	h := newHarness(t, roleperm.Default())
	uid := h.seedUser("owner@foldex.test", "owner")

	require.NoError(t, backupagent.NewScheduleStore(h.pool).Heartbeat(context.Background(), backupagent.AgentState{
		SeenAt:  time.Now().UTC(),
		Version: "2.17.0",
		// One BEHIND the backend: the skew the band exists to name.
		SchemaVersion: backupagent.RequiredSchemaVersion - 1,
		Jobs: map[string]backupagent.JobReport{
			"drill": {Capable: false, Reason: "no_identity", Source: "env", Schedule: "disabled"},
		},
	}))

	rec := h.doAs(uid, authctx.RoleOwner, http.MethodGet, "/api/admin/backup/schedule", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body := decode(t, rec)
	agent, _ := body["agent"].(map[string]any)
	require.NotNil(t, agent)
	assert.Equal(t, "2.17.0", agent["version"])
	// Both numbers travel, so the client compares rather than re-deriving a
	// policy the server already knows (INV-138's reasoning).
	assert.EqualValues(t, backupagent.RequiredSchemaVersion-1, agent["schema_version"])
	assert.EqualValues(t, backupagent.RequiredSchemaVersion, body["agent_schema_version"])
	jobs, _ := agent["jobs"].(map[string]any)
	drill, _ := jobs["drill"].(map[string]any)
	assert.Equal(t, false, drill["capable"])
	assert.Equal(t, "no_identity", drill["reason"],
		"the UI must be able to say WHY a job cannot be scheduled, not just grey it out")
}

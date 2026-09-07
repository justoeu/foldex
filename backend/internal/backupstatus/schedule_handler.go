package backupstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/backupjobs"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// What one agenda edit is recorded as. The event name is the auth package's
// (AuditBackupScheduleChanged); these are the words inside the detail.
const (
	scheduleActionSet   = "set"
	scheduleActionReset = "reset to the env baseline"
	// scheduleBaseline: there was no row, so the job was on the env agenda.
	scheduleBaseline = "env baseline"
	// scheduleUnknown: the read itself failed. Not the same fact as "there
	// was no row", and the trail must not pass one off as the other.
	scheduleUnknown = "unknown"
)

// GetSchedule answers the agenda screen in one request: the stored rows (the
// editable layer), the agent's heartbeat (the truth about what will actually
// run — capability, source and rendered schedule per job), and the compiled
// bounds so the form can refuse locally what the server would refuse anyway.
func (h *Handler) GetSchedule(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.Schedule(r.Context())
	if err != nil {
		h.logger.Error("backup schedule read", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	agent, seen, err := h.repo.AgentState(r.Context())
	if err != nil {
		h.logger.Error("backup agent state read", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	out := scheduleResponse{
		Jobs: Jobs,
		Rows: rows,
		Bounds: scheduleBounds{
			TimesMin:        backupjobs.MinTimes,
			TimesMax:        backupjobs.MaxTimes,
			WeekdaysMin:     backupjobs.MinWeekdays,
			DumpWeekdaysMin: backupjobs.MinDumpWeekdays,
			IntervalMin:     backupjobs.MinIntervalMin,
			IntervalMax:     backupjobs.MaxIntervalMin,
		},
		// The document shape THIS backend writes. Paired with the heartbeat's
		// own schema_version it lets the band say "the agent predates the
		// current agenda format" — a skew that is otherwise silent, because
		// backupjobs.RequiredSchemaVersion is a floor: an older agent boots
		// fine on a newer schema and simply ignores the fields it never
		// learned. The client COMPARES the two numbers; it does not re-derive
		// the policy (INV-138).
		AgentSchemaVersion: backupjobs.RequiredSchemaVersion,
	}
	// null, not a zero struct: "no agent ever wrote a heartbeat" is the
	// honest empty state the band renders as "agente nunca visto" — a zero
	// SeenAt would render as 1970 and look like a bug instead of a fact.
	if seen {
		out.Agent = &agent
	}
	httperr.JSON(w, http.StatusOK, out)
}

// scheduleResponse is the GET /schedule document. Field names and json tags
// match BackupScheduleResponse; encoding an open map let a dropped key
// (agent_schema_version) compile and ship.
type scheduleResponse struct {
	Jobs               []string                          `json:"jobs"`
	Rows               map[string]backupjobs.ScheduleRow `json:"rows"`
	Bounds             scheduleBounds                    `json:"bounds"`
	Agent              *backupjobs.AgentState            `json:"agent"`
	AgentSchemaVersion int                               `json:"agent_schema_version"`
}

type scheduleBounds struct {
	TimesMin        int `json:"times_min"`
	TimesMax        int `json:"times_max"`
	WeekdaysMin     int `json:"weekdays_min"`
	DumpWeekdaysMin int `json:"dump_weekdays_min"`
	IntervalMin     int `json:"interval_min"`
	IntervalMax     int `json:"interval_max"`
}

// PutSchedule stores one job's agenda row. The floors live in
// backupjobs.ValidateJobConfig — the same function the agent applies when it
// loads, so what saves here is exactly what runs there.
func (h *Handler) PutSchedule(w http.ResponseWriter, r *http.Request) {
	job, ok := CanonicalJob(chi.URLParam(r, "job"))
	if !ok {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_job",
			"job must be one of dump, drill, mirror, user_zip"))
		return
	}
	in, err := httperr.DecodeJSON[backupjobs.JobConfig](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := backupjobs.ValidateJobConfig(job, in); err != nil {
		// The message names the field and its bounds — documented limits,
		// not secrets, and an owner told the real floor can fix the form
		// (INV-169's reasoning).
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_schedule", err.Error()))
		return
	}
	var by int64
	if p, ok := authctx.FromContext(r.Context()); ok {
		by = int64(p.UserID)
	}
	// Read before writing: once the upsert lands, what the agenda used to say
	// exists nowhere else.
	var before string
	if h.auditSchedule != nil {
		before = h.storedSchedule(r.Context(), job)
	}
	if err := h.repo.SetSchedule(r.Context(), job, in, by); err != nil {
		// SetSchedule takes the request document; logging err re-taints the
		// sink (CodeQL go/log-injection) the same way abuse policy Set did.
		// The interned job is already on the audit trail when that path runs.
		h.logger.Error("backup schedule set")
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if h.auditSchedule != nil {
		h.auditSchedule(r, scheduleAudit(job, scheduleActionSet, before, renderSchedule(in)))
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"job": job, "config": in})
}

// DeleteSchedule removes one job's row — the agent falls back to the env
// baseline on its next sync. Idempotent: deleting an absent row is the state
// the caller asked for.
func (h *Handler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	job, ok := CanonicalJob(chi.URLParam(r, "job"))
	if !ok {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_job",
			"job must be one of dump, drill, mirror, user_zip"))
		return
	}
	var before string
	if h.auditSchedule != nil {
		before = h.storedSchedule(r.Context(), job)
	}
	if err := h.repo.DeleteSchedule(r.Context(), job); err != nil {
		h.logger.Error("backup schedule delete", "err", err, "job", job)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if h.auditSchedule != nil {
		// What was reset AWAY is the whole content of a delete: the row is
		// gone from backup_schedule and the trail is the only place left
		// that can say what it held.
		h.auditSchedule(r, scheduleAudit(job, scheduleActionReset, before, scheduleBaseline))
	}
	w.WriteHeader(http.StatusNoContent)
}

// scheduleAudit is the detail one agenda change is recorded under. The event
// name alone was enough while a PUT moved a single wall time; ADR-45 lets one
// request change the mode, the weekday set and every time at once, and a trail
// that cannot answer "what was the agenda during the incident" is not the
// durable record INV-047 asks for.
func scheduleAudit(job, action, before, after string) string {
	return fmt.Sprintf("%s schedule %s: %s → %s", job, action, before, after)
}

// renderSchedule is one agenda document as the trail stores it. JSON rather
// than Timing.String(): a line someone reads a year later has to be enough to
// reconstruct the row, and the display form drops "enabled" and the mode.
func renderSchedule(cfg backupjobs.JobConfig) string {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return scheduleUnknown
	}
	return string(raw)
}

// storedSchedule is the agenda a job carries right now, for the "before" half
// of the record. A read failure never refuses the edit — it is recorded as
// unknown, because losing the write over a trail lookup would be a worse
// trade than an incomplete line.
func (h *Handler) storedSchedule(ctx context.Context, job string) string {
	rows, err := h.repo.Schedule(ctx)
	if err != nil {
		h.logger.Warn("backup schedule audit read", "err", err, "job", job)
		return scheduleUnknown
	}
	row, ok := rows[job]
	if !ok {
		return scheduleBaseline
	}
	return renderSchedule(row.Config)
}

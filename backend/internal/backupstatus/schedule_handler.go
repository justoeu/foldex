package backupstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/backupagent"
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
	out := map[string]any{
		"jobs": Jobs,
		"rows": rows,
		"bounds": map[string]int{
			"times_min":         backupagent.MinTimes,
			"times_max":         backupagent.MaxTimes,
			"weekdays_min":      backupagent.MinWeekdays,
			"dump_weekdays_min": backupagent.MinDumpWeekdays,
			"interval_min":      backupagent.MinIntervalMin,
			"interval_max":      backupagent.MaxIntervalMin,
		},
	}
	// null, not a zero struct: "no agent ever wrote a heartbeat" is the
	// honest empty state the band renders as "agente nunca visto" — a zero
	// SeenAt would render as 1970 and look like a bug instead of a fact.
	if seen {
		out["agent"] = agent
	} else {
		out["agent"] = nil
	}
	httperr.JSON(w, http.StatusOK, out)
}

// PutSchedule stores one job's agenda row. The floors live in
// backupagent.ValidateJobConfig — the same function the agent applies when it
// loads, so what saves here is exactly what runs there.
func (h *Handler) PutSchedule(w http.ResponseWriter, r *http.Request) {
	job := chi.URLParam(r, "job")
	if !ValidJob(job) {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_job",
			"job must be one of dump, drill, mirror, user_zip"))
		return
	}
	in, err := httperr.DecodeJSON[backupagent.JobConfig](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := backupagent.ValidateJobConfig(job, in); err != nil {
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
		h.logger.Error("backup schedule set", "err", err, "job", job)
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
	job := chi.URLParam(r, "job")
	if !ValidJob(job) {
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
func renderSchedule(cfg backupagent.JobConfig) string {
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

package auth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/abusepolicy"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/authgate"
	"foldex/internal/pkg/httperr"
)

// AbuseHandler serves the abuse-defence surface — ADR-47.
//
// Its own type rather than more methods on AdminHandler, for policy.Handler's
// reason: this one needs the abuse policy store and the enforcement cache, and
// neither belongs in a constructor that already takes six arguments and is
// built at three call sites. It mounts INSIDE the /api/admin group, so the
// role gate has already answered 404 to a non-administrator (INV-043) and the
// API-token gate has already run.
type AbuseHandler struct {
	// repo reads the trail. The detector is a query over audit_log, so it
	// belongs to the repository that owns that table rather than to a second
	// one pointed at the same rows.
	repo   *Repository
	policy *abusepolicy.Repository
	// cache is the live policy every enforcement site reads. A write
	// invalidates it so the new limits apply on the next request instead of up
	// to a TTL later — the person who clicked save is watching. Nil is legal
	// (a build with no enforcement wired) and simply skips the nudge.
	cache  *abusepolicy.Cache
	logger *slog.Logger
	// audit records the edit. A function rather than the AdminHandler, matching
	// how internal/policy and internal/backupstatus receive theirs, so the one
	// place that resolves the actor's identity stays one place.
	audit  func(*http.Request, string)
	grants authgate.Grants
}

func NewAbuseHandler(repo *Repository, policy *abusepolicy.Repository,
	cache *abusepolicy.Cache, logger *slog.Logger,
	audit func(*http.Request, string), grants authgate.Grants) *AbuseHandler {
	return &AbuseHandler{repo: repo, policy: policy, cache: cache,
		logger: logger, audit: audit, grants: grants}
}

// Mount registers the surface on the administration group.
//
// Reading is any administrator's job — an admin has to be able to see the
// rules the instance is defended under, exactly as they may read the password
// policy. WRITING is instance.rate_limits, which is owner-only and LOCKED:
// both directions of a wrong value are a denial of service the holder
// installs, and the seat that cannot be locked out of its own instance is the
// only one that can safely repair one.
func (h *AbuseHandler) Mount(r chi.Router) {
	read := authgate.RequirePermission(h.grants, authctx.PermPolicyRead)
	write := authgate.RequirePermission(h.grants, authctx.PermInstanceRateLimits)
	r.With(read).Get("/abuse-policy", h.GetPolicy)
	r.With(write).Put("/abuse-policy", h.PutPolicy)
	// The panel reads the trail, so it is gated on the permission that governs
	// reading the trail rather than on the one that governs editing limits: a
	// role able to see who signed in can see which origin is sweeping accounts.
	r.With(authgate.RequirePermission(h.grants, authctx.PermAuditRead)).
		Get("/anomalies", h.Anomalies)
}

// GetPolicy serves the document, its bounds, what the instance actually did,
// and whether this caller may save.
func (h *AbuseHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := h.policy.Get(r.Context())
	if err != nil {
		h.logger.Error("abuse policy get", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// A failure to measure must not fail the screen. The observations are the
	// recommendation's input, not the policy; answering 500 because an
	// aggregate was unavailable would hide the form during exactly the incident
	// an owner opened it for. Zeroes render as "no data", which is what an
	// unavailable measurement honestly is.
	observed, err := h.repo.AbuseObservedSince(r.Context())
	if err != nil {
		h.logger.Warn("abuse observed", "err", err)
		observed = AbuseObserved{Days: AbuseObservedDays}
	}
	httperr.JSON(w, http.StatusOK, map[string]any{
		"policy":    p,
		"bounds":    abusepolicy.Bounds(),
		"observed":  observed,
		"can_write": h.canWrite(r),
	})
}

// canWrite answers what the screen needs to render the form disabled WITH a
// reason, instead of offering a save that always 403s. It asks the same matrix
// the gate asks, so the two cannot disagree.
func (h *AbuseHandler) canWrite(r *http.Request) bool {
	p, ok := authctx.FromContext(r.Context())
	return ok && h.grants != nil && h.grants.Can(p.Role, authctx.PermInstanceRateLimits)
}

// PutPolicy saves the limits.
func (h *AbuseHandler) PutPolicy(w http.ResponseWriter, r *http.Request) {
	current, err := h.policy.Get(r.Context())
	if err != nil {
		h.logger.Error("abuse policy get", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// Decoded ON TOP of the stored document rather than into a zero value: a
	// client that predates a knob would otherwise send nothing for it and
	// submit a zero, which ValidateForWrite refuses — so adding a knob would
	// break every older screen's save. Merging makes an absent field mean
	// "leave it as it is", which is also what the form's own partial saves
	// mean.
	in, derr := decodeInto(w, r, current)
	if derr != nil {
		httperr.Write(w, derr)
		return
	}
	// ValidateForWrite runs inside Set, and its message is returned VERBATIM:
	// it names the field and the real bounds, which are documented limits
	// rather than secrets. An owner told the real ceiling can fix the form; one
	// told "invalid" is left guessing, which is what INV-169's handler-side
	// refusal exists to avoid.
	if err := in.ValidateForWrite(); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_policy", err.Error()))
		return
	}
	if err := h.policy.Set(r.Context(), in); err != nil {
		h.logger.Error("abuse policy set", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.cache.Invalidate()
	if h.audit != nil {
		h.audit(r, fmt.Sprintf(
			"abuse limits updated: login %d accounts/%d failures per %d min, "+
				"api %d writes/min, %d expensive/h, click coalesce %ds",
			in.LoginDistinctAccountsPerIP, in.LoginFailuresPerAccount, in.LoginWindowMinutes,
			in.APIWritesPerMinute, in.APIExpensivePerHour, in.ClickCoalesceSeconds()))
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"policy": in})
}

// decodeInto merges a request body over an existing document.
//
// httperr.DecodeJSON decodes into a zero value, which is the wrong base here;
// everything else about the read is identical — the same 64 KiB cap (INV-089)
// and the same refusal of unknown fields, so a typo in a knob name is a 400
// rather than a save that silently drops it.
func decodeInto(w http.ResponseWriter, r *http.Request,
	base abusepolicy.Policy) (abusepolicy.Policy, error) {
	r.Body = http.MaxBytesReader(w, r.Body, httperr.JSONBodyCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&base); err != nil {
		return base, httperr.New(http.StatusBadRequest, "invalid_json", err.Error())
	}
	return base, nil
}

// Anomalies serves the ranked findings for one window.
func (h *AbuseHandler) Anomalies(w http.ResponseWriter, r *http.Request) {
	p, err := h.policy.Get(r.Context())
	if err != nil {
		h.logger.Error("abuse policy get", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	window, label, ok := anomalyWindow(r.URL.Query().Get("window"), p.AnomalyWindowMinutes)
	if !ok {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_window",
			"window must be 15m, 1h, 24h or 7d"))
		return
	}
	th := AnomalyThresholds{
		SprayAccounts:  p.AnomalySprayAccounts,
		HammerFailures: p.AnomalyHammerFailures,
		WindowMinutes:  int(window / time.Minute),
	}
	found, err := h.repo.Anomalies(r.Context(), window, th)
	if err != nil {
		h.logger.Error("anomalies", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// Never nil: the screen renders a list, and `null` would make it render
	// nothing rather than its quiet state.
	rows := rankAnomalies(found, maxAnomalyRows, h.logger)
	if rows == nil {
		rows = []Anomaly{}
	}
	httperr.JSON(w, http.StatusOK, map[string]any{
		"window":     label,
		"thresholds": th,
		"anomalies":  rows,
	})
}

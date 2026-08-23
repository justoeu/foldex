package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// availability is the one answer both probes give. `reason` names WHY a value
// is unusable, because "unavailable" alone sends the user back to the field
// with nothing to change — a reserved name and a taken one need different
// fixes, and a malformed one needs neither.
type availability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// The closed set `reason` may hold. Named constants rather than literals
// because these values are part of the API contract — SDD-AUTH-RBAC documents
// them — and four bare strings across two handlers cannot be grepped with the
// client union that mirrors them.
const (
	reasonShape    = "shape"
	reasonReserved = "reserved"
	reasonTaken    = "taken"
	reasonEmpty    = "empty"
	// reasonPending rides an AVAILABLE answer: somebody is moving to this
	// address but has not confirmed, which the create may still win.
	reasonPending = "pending"
)

// UsernameAvailable answers whether the signed-in account could claim a
// username, while it is being typed.
//
// This IS an enumeration oracle, and a cheap one — no password, one round-trip.
// Three things are what make it acceptable, and none of them is optional:
//
//  1. It is session-only. An anonymous caller cannot reach it, so the property
//     login protects with a single 401, an always-run bcrypt and a 250 ms floor
//     is untouched.
//  2. It answers about USERNAMES and never about e-mail. An address is also a
//     mailbox and exists outside this instance; a username exists only here, so
//     confirming one is taken says "somebody here uses that handle" rather than
//     "this person has an account here".
//  3. It is capped per user (`availabilityUser`).
//
// The caller's own current username is available to them, or the form would
// report "taken" about the name the user is looking at.
func (h *Handler) UsernameAvailable(w http.ResponseWriter, r *http.Request) {
	// The one handler in this package where a zero principal would be worse than
	// a 500: it would both answer the question AND file every caller under a
	// single `avail:0` bucket, collapsing the per-user cap that is half the
	// safety argument. The route is mounted behind Authenticate, so this cannot
	// fire today — it is here so that a future remount onto `Optional` fails
	// closed instead of open.
	p, ok := authctx.FromContext(r.Context())
	if !ok || p.UserID == 0 {
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	raw := strings.TrimSpace(r.URL.Query().Get("u"))
	if raw == "" {
		httperr.JSON(w, http.StatusOK, availability{Reason: reasonEmpty})
		return
	}
	// Charged BEFORE the lookup, and charged for malformed input too. Skipping
	// the shape-refused ones would let a script probe for free by appending a
	// character the validator rejects — the same reasoning that makes the login
	// bucket increment for addresses that do not exist.
	// CommitFail is how this limiter charges: a probe has no success/failure
	// outcome, so every answer costs one. Retry-After is carried because this
	// lockout is reachable in ordinary use, and "wait five minutes" is the only
	// thing that distinguishes it from a broken instance.
	//
	// A request the client ABORTS mid-flight has already been charged, and that
	// is deliberate rather than an oversight. Charging after the lookup would
	// mean a caller who hangs up before the answer pays nothing — which is the
	// whole enumeration budget, since nothing forces an attacker to read the
	// response body to learn from the timing of the connection. The cost is
	// borne only when the server is slower than the client's 450 ms debounce,
	// which is rare and self-correcting; the alternative is a cap that anyone
	// can opt out of.
	until, admitted := h.availabilityUser.Begin(userBucketKey("avail", p.UserID))
	if !admitted {
		if wait := time.Until(until); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
		httperr.Write(w, httperr.New(http.StatusTooManyRequests, "too_many_attempts",
			"too many checks — wait a moment"))
		return
	}
	h.availabilityUser.CommitFail(userBucketKey("avail", p.UserID))

	norm, err := NormalizeUsername(raw)
	if err != nil {
		// NormalizeUsername collapses "wrong shape" and "reserved" into one
		// error on purpose — the write path needs only "invalid". A form has to
		// tell them apart: someone who typed `admin` deserves better than
		// "wrong characters". The predicate is shared rather than re-derived,
		// so the classification cannot go stale behind the rule.
		reason := reasonShape
		if ReservedUsername(strings.ToLower(raw)) {
			reason = reasonReserved
		}
		httperr.JSON(w, http.StatusOK, availability{Reason: reason})
		return
	}

	free, err := h.repo.UsernameAvailable(r.Context(), p.UserID, norm)
	if err != nil {
		h.logger.Error("username availability", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if !free {
		httperr.JSON(w, http.StatusOK, availability{Reason: reasonTaken})
		return
	}
	httperr.JSON(w, http.StatusOK, availability{Available: true})
}

// EmailAvailable answers whether an address is free, for the administrator
// creating an account.
//
// Mounted under /api/admin ONLY, and that placement is the whole safety
// argument: past RequireAdmin the caller can already list every account with
// its address, so this discloses nothing they could not read directly, and it
// is uncapped for the same reason. It must never be mirrored onto a route an
// ordinary session or an anonymous caller can reach — the e-mail-change flow
// deliberately keeps its answer at submit time, where a password is the cost of
// each guess.
func (h *AdminHandler) EmailAvailable(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("email"))
	if raw == "" {
		httperr.JSON(w, http.StatusOK, availability{Reason: reasonEmpty})
		return
	}
	if err := validateEmail(raw); err != nil {
		httperr.JSON(w, http.StatusOK, availability{Reason: reasonShape})
		return
	}
	taken, pending, err := h.repo.EmailAvailable(r.Context(), NormalizeEmail(raw))
	if err != nil {
		h.logger.Error("email availability", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	switch {
	case taken:
		httperr.JSON(w, http.StatusOK, availability{Reason: reasonTaken})
	case pending:
		// Available AND flagged. The create would succeed, so refusing it would
		// hide a working button; but the administrator should know they are
		// racing somebody who has already proved control of that mailbox.
		httperr.JSON(w, http.StatusOK, availability{Available: true, Reason: reasonPending})
	default:
		httperr.JSON(w, http.StatusOK, availability{Available: true})
	}
}

// userBucketKey names a per-user rate-limit bucket.
//
// Five call sites built this string inline, two of them repeating the same
// `"stepup-password:"` prefix verbatim. The prefix matters even though each
// limiter is a separate map: it is what stops two budgets from sharing a
// namespace the first time someone reuses a limiter for a second purpose.
func userBucketKey(prefix string, uid authctx.UserID) string {
	return prefix + ":" + strconv.FormatInt(int64(uid), 10)
}

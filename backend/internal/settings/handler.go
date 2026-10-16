package settings

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/attemptlimit"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// The master password is the recovery root for every protected folder, so
// proving it here spends from a per-user budget with the folder unlock's
// shape: five wrong proofs, then an hour of refusals. Without it this route
// (and the folder reset route, which has its own) is an unthrottled oracle
// for the one secret that opens everything.
const (
	maxMasterAttempts = 5
	masterLockout     = time.Hour
)

type Handler struct {
	repo    *Repository
	limiter *attemptlimit.Limiter
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo, limiter: attemptlimit.New(maxMasterAttempts, masterLockout)}
}

// WithMasterLimiter shares one master-password budget with the folders
// handler: the secret is one, so its guessing budget must be one too —
// splitting it per surface would double the guesses per hour.
func (h *Handler) WithMasterLimiter(l *attemptlimit.Limiter) *Handler {
	h.limiter = l
	return h
}

const pathMasterPassword = "/master-password"

func (h *Handler) Mount(r chi.Router) {
	r.Get(pathMasterPassword, h.status)
	r.Put(pathMasterPassword, h.setMaster)
	r.Delete(pathMasterPassword, h.clearMaster)
}

// SweepLimiters is the lifecycle hook registered with the process sweeper.
func (h *Handler) SweepLimiters(olderThan time.Duration) int {
	return h.limiter.Sweep(olderThan)
}

func masterKeyFor(uid authctx.UserID) string {
	return "master:" + strconv.FormatInt(int64(uid), 10)
}

func writeMasterRateLimited(w http.ResponseWriter, until time.Time) {
	retry := int(time.Until(until).Seconds())
	if retry < 1 {
		retry = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	httperr.Write(w, httperr.New(http.StatusTooManyRequests, "too_many_attempts",
		"too many attempts; try again later"))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	configured, err := h.repo.MasterPasswordConfigured(r.Context(), authctx.MustUser(r.Context()))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	var hint *string
	if configured {
		if hint, err = h.repo.MasterPasswordHint(r.Context(), authctx.MustUser(r.Context())); err != nil {
			httperr.Write(w, err)
			return
		}
	}
	httperr.JSON(w, http.StatusOK, statusOutput{Configured: configured, Hint: hint})
}

func (h *Handler) setMaster(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[setMasterInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if err := in.Validate(); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_input", err.Error()))
		return
	}
	uid := authctx.MustUser(r.Context())
	key := masterKeyFor(uid)
	if until, ok := h.limiter.Begin(key); !ok {
		writeMasterRateLimited(w, until)
		return
	}
	// Tri-state: hint field absent (in.Hint == nil) → keep the existing hint;
	// present → set the trimmed value (empty string clears it). Distinguishing
	// absent from empty is why we don't collapse to NormalizedHint here.
	var hintArg *string
	if in.Hint != nil {
		s := strings.TrimSpace(*in.Hint)
		hintArg = &s
	}
	// The configured check and the current-password proof run inside the
	// repository under the app_user row lock; the handler only settles the
	// limiter reservation with the outcome.
	err = h.repo.SetMasterPassword(r.Context(), uid, in.Password, in.CurrentPassword, hintArg)
	switch {
	case errors.Is(err, ErrCurrentPasswordRequired):
		h.limiter.Release(key)
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "wrong_password", "current master password is required to change it"))
		return
	case errors.Is(err, ErrWrongMasterPassword):
		if _, lockedUntil := h.limiter.CommitFail(key); !lockedUntil.IsZero() {
			writeMasterRateLimited(w, lockedUntil)
			return
		}
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "wrong_password", "current master password is incorrect"))
		return
	case errors.Is(err, ErrHintMatchesPassword):
		h.limiter.Release(key)
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_input", err.Error()))
		return
	case err != nil:
		h.limiter.Release(key)
		httperr.Write(w, err)
		return
	default:
		h.limiter.CommitSuccess(key)
	}
	resHint, err := h.repo.MasterPasswordHint(r.Context(), uid)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, statusOutput{Configured: true, Hint: resHint})
}

func (h *Handler) clearMaster(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[clearMasterInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	uid := authctx.MustUser(r.Context())
	key := masterKeyFor(uid)
	if until, ok := h.limiter.Begin(key); !ok {
		writeMasterRateLimited(w, until)
		return
	}
	configured, err := h.repo.ClearMasterPassword(r.Context(), uid, in.CurrentPassword)
	switch {
	case errors.Is(err, ErrWrongMasterPassword):
		if _, lockedUntil := h.limiter.CommitFail(key); !lockedUntil.IsZero() {
			writeMasterRateLimited(w, lockedUntil)
			return
		}
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "wrong_password", "current master password is incorrect"))
		return
	case err != nil:
		h.limiter.Release(key)
		httperr.Write(w, err)
		return
	case configured:
		h.limiter.CommitSuccess(key)
	default:
		// Nothing was configured: no guess was tested.
		h.limiter.Release(key)
	}
	httperr.JSON(w, http.StatusOK, statusOutput{Configured: false})
}

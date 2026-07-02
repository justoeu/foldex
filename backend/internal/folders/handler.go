package folders

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"
)

// MasterPasswordVerifier is the narrow slice of the settings repository the
// folder reset route needs: verify the operator's master recovery password.
// Defined here (consumer-side) so folders doesn't import settings — settings
// satisfies it, avoiding an import cycle. configured=false means no master is
// set (recovery disabled); ok=false with configured=true means wrong password.
type MasterPasswordVerifier interface {
	VerifyMaster(ctx context.Context, plain string) (ok bool, configured bool, err error)
}

type Handler struct {
	repo      *Repository
	unlockKey []byte
	master    MasterPasswordVerifier
	limiter   *unlockLimiter
}

// NewHandler takes the folder-unlock-token HMAC secret (see
// LoadOrGenerateFolderUnlockKey) so it can gate list(parent_id=X) and mint/
// verify tokens for the /unlock endpoint, plus a MasterPasswordVerifier used
// only by the master-password recovery route.
func NewHandler(repo *Repository, unlockKey []byte, master MasterPasswordVerifier) *Handler {
	return &Handler{repo: repo, unlockKey: unlockKey, master: master, limiter: newUnlockLimiter()}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
	r.Post("/{id}/unlock", h.unlock)
	r.Post("/{id}/reset-password", h.resetPassword)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := ListQuery{}
	if v := r.URL.Query().Get("parent_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			q.ParentID = &n
		}
	}
	if v := r.URL.Query().Get("root"); v == "1" || v == "true" {
		q.RootOnly = true
	}
	// Content-gate: listing a protected folder's CHILDREN reveals its
	// contents just as much as reading its links would, so it needs the
	// same unlock-token proof. Root/flat listings (ParentID == nil) are
	// never gated — only each protected folder's own preview_links/
	// preview_folders are redacted there (see Repository.List).
	if q.ParentID != nil {
		hash, err := h.repo.PasswordHashFor(r.Context(), *q.ParentID)
		if err != nil {
			httperr.Write(w, err)
			return
		}
		if err := CheckUnlock(h.unlockKey, *q.ParentID, hash, r.Header.Get(UnlockHeader)); err != nil {
			httperr.Write(w, err)
			return
		}
	}
	out, err := h.repo.List(r.Context(), q)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, out)
}

type unlockInput struct {
	Password string `json:"password"`
}

type unlockOutput struct {
	UnlockToken string    `json:"unlock_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// errEnvelope mirrors httperr's wire shape so the unlock endpoint can attach
// extra fields (attempt counters, lockout expiry) alongside the standard error
// without changing the shared envelope every other handler uses.
type errEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type unlockWrongOutput struct {
	Error             errEnvelope `json:"error"`
	FailedAttempts    int         `json:"failed_attempts"`
	AttemptsRemaining int         `json:"attempts_remaining"`
}

type unlockLockedOutput struct {
	Error             errEnvelope `json:"error"`
	LockedUntil       time.Time   `json:"locked_until"`
	RetryAfterSeconds int         `json:"retry_after_seconds"`
}

func (h *Handler) writeLocked(w http.ResponseWriter, until time.Time) {
	retry := int(time.Until(until).Seconds())
	if retry < 1 {
		retry = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	httperr.JSON(w, http.StatusTooManyRequests, unlockLockedOutput{
		Error:             errEnvelope{Code: "too_many_attempts", Message: "too many wrong attempts; folder temporarily locked"},
		LockedUntil:       until,
		RetryAfterSeconds: retry,
	})
}

func (h *Handler) unlock(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// Lockout check comes BEFORE reading the body / hashing so a locked-out
	// folder costs nothing and can't be probed.
	if until := h.limiter.lockedUntil(id); !until.IsZero() {
		h.writeLocked(w, until)
		return
	}
	in, err := httperr.DecodeJSON[unlockInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	hash, err := h.repo.PasswordHashFor(r.Context(), id)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if hash == nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "not_protected", "this folder has no password set"))
		return
	}
	if !VerifyPassword(*hash, in.Password) {
		fails, lockedUntil := h.limiter.fail(id)
		if !lockedUntil.IsZero() {
			h.writeLocked(w, lockedUntil)
			return
		}
		remaining := maxUnlockAttempts - fails
		if remaining < 0 {
			remaining = 0
		}
		httperr.JSON(w, http.StatusUnauthorized, unlockWrongOutput{
			Error:             errEnvelope{Code: "wrong_password", Message: "incorrect password"},
			FailedAttempts:    fails,
			AttemptsRemaining: remaining,
		})
		return
	}
	h.limiter.reset(id)
	httperr.JSON(w, http.StatusOK, unlockOutput{
		UnlockToken: IssueUnlockToken(h.unlockKey, id, *hash),
		ExpiresAt:   time.Now().Add(unlockTokenTTL),
	})
}

type resetPasswordInput struct {
	MasterPassword string `json:"master_password"`
}

// resetPassword is the master-password RECOVERY route (ADR-29): given the
// correct master password, it clears the folder's password + hint so a new one
// can be set via the normal first-time-set flow. It never unlocks the folder
// for viewing and never mints an unlock token — recovery only. 400
// master_not_configured when no master is set; 401 wrong_master_password on
// mismatch.
func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in, err := httperr.DecodeJSON[resetPasswordInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	ok, configured, err := h.master.VerifyMaster(r.Context(), in.MasterPassword)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	if !configured {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "master_not_configured", "no master password is configured; set one in Settings first"))
		return
	}
	if !ok {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "wrong_master_password", "incorrect master password"))
		return
	}
	if err := h.repo.ResetPasswordByMaster(r.Context(), id); err != nil {
		httperr.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	in, err := httperr.DecodeJSON[CreateInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in.Normalize()
	if err := in.Validate(); err != nil {
		var v validationErr
		if errors.As(err, &v) {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_input", string(v)))
			return
		}
		httperr.Write(w, err)
		return
	}
	f, err := h.repo.Create(r.Context(), in)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusCreated, f)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	f, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, f)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in, err := httperr.DecodeJSON[UpdateInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	in.Normalize()
	if err := in.Validate(); err != nil {
		var v validationErr
		if errors.As(err, &v) {
			httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_input", string(v)))
			return
		}
		httperr.Write(w, err)
		return
	}
	f, err := h.repo.Update(r.Context(), id, in)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusOK, f)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// ?cascade=1|true → delete contained links too. Default keeps links (the
	// FK is ON DELETE SET NULL so they unflag back to ungrouped on home).
	cascade := false
	if v := r.URL.Query().Get("cascade"); v == "1" || v == "true" {
		cascade = true
	}
	if cascade {
		if err := h.repo.DeleteCascade(r.Context(), id); err != nil {
			httperr.Write(w, err)
			return
		}
	} else {
		if err := h.repo.Delete(r.Context(), id); err != nil {
			httperr.Write(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

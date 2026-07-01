package folders

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"
)

type Handler struct {
	repo      *Repository
	unlockKey []byte
}

// NewHandler takes the folder-unlock-token HMAC secret (see
// LoadOrGenerateFolderUnlockKey) so it can gate list(parent_id=X) and mint/
// verify tokens for the /unlock endpoint.
func NewHandler(repo *Repository, unlockKey []byte) *Handler {
	return &Handler{repo: repo, unlockKey: unlockKey}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
	r.Post("/{id}/unlock", h.unlock)
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

func (h *Handler) unlock(w http.ResponseWriter, r *http.Request) {
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
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
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "wrong_password", "incorrect password"))
		return
	}
	httperr.JSON(w, http.StatusOK, unlockOutput{
		UnlockToken: IssueUnlockToken(h.unlockKey, id, *hash),
		ExpiresAt:   time.Now().Add(unlockTokenTTL),
	})
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

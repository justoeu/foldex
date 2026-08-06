package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// maxTokenNameLen bounds the label. The name is chosen by the owner and shown
// back to them; it is not a security boundary, only a cap on what one account
// can store.
const maxTokenNameLen = 100

// maxTokensPerUser bounds how many live tokens one account may hold.
//
// Not a rate limit — the endpoint is session-authenticated, so there is no
// anonymous abuse to stop. It is there because the token list is a security
// surface a person has to READ: an account with two hundred entries is one
// where a rogue token goes unnoticed.
const maxTokensPerUser = 20

type createTokenInput struct {
	Name string `json:"name"`
	// ExpiresInDays is optional. Zero means "no expiry", which is the honest
	// default for the browser extension: an extension that stops working every
	// 90 days trains its user to click through whatever re-authorisation prompt
	// appears, which is worse than the long-lived credential.
	ExpiresInDays int `json:"expires_in_days"`
}

// ListAPITokens returns the caller's live tokens.
func (h *Handler) ListAPITokens(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	list, err := h.repo.ListAPITokens(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("list api tokens", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"tokens": list})
}

// CreateAPIToken mints a bearer credential and returns it ONCE.
//
// No password is required, and that is a deliberate line: the token's scope is
// `content`, exactly what the live session already grants. Demanding a
// step-up for a credential no more powerful than the one presenting the request
// would be ceremony. The endpoints where a token WOULD be an escalation —
// password, invites, admin, backup — refuse tokens outright instead.
func (h *Handler) CreateAPIToken(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	in, err := httperr.DecodeJSON[createTokenInput](w, r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTokenNameLen {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_name",
			"give the token a short name so you can recognise it later"))
		return
	}
	if in.ExpiresInDays < 0 {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_expiry",
			"expiry must not be negative"))
		return
	}

	existing, err := h.repo.ListAPITokens(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("count api tokens", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if len(existing) >= maxTokensPerUser {
		httperr.Write(w, httperr.New(http.StatusConflict, "too_many_tokens",
			"revoke an existing token before creating another"))
		return
	}

	tok, err := h.repo.CreateAPIToken(r.Context(), p.UserID, name,
		time.Duration(in.ExpiresInDays)*24*time.Hour)
	if err != nil {
		h.logger.Error("create api token", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// The plaintext is in this response and nowhere else — the server stores
	// only sha256, so "show it again" is not a feature that was left out, it is
	// one that cannot exist.
	httperr.JSON(w, http.StatusCreated, tok)
}

// RevokeAPIToken kills one of the caller's tokens.
func (h *Handler) RevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.FromContext(r.Context())
	id, err := httperr.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		httperr.Write(w, err)
		return
	}
	// A token belonging to someone else is reported 404, never 403 — the same
	// rule content rows follow, and for the same reason: a 403 confirms the id
	// exists and turns a dense BIGSERIAL space into an enumeration oracle.
	if err := h.repo.RevokeAPIToken(r.Context(), p.UserID, id); err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			httperr.Write(w, httperr.ErrNotFound)
			return
		}
		h.logger.Error("revoke api token", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

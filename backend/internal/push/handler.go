package push

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"
)

// Handler exposes the HTTP surface needed by the frontend subscribe flow:
//
//	GET    /api/push/vapid-key       → returns the VAPID public key so the
//	                                   browser can pass it to PushManager.subscribe
//	POST   /api/push/subscriptions   → upsert a subscription
//	DELETE /api/push/subscriptions   → remove a subscription by endpoint
//	POST   /api/push/test            → send a "hello from foldex" to every
//	                                   live subscription (useful when wiring
//	                                   client-side: confirms VAPID + SW work).
type Handler struct {
	keys   VAPIDKeys
	repo   *Repository
	sender *Sender
}

func NewHandler(keys VAPIDKeys, repo *Repository, sender *Sender) *Handler {
	return &Handler{keys: keys, repo: repo, sender: sender}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/vapid-key", h.vapidKey)
	r.Post("/subscriptions", h.subscribe)
	r.Delete("/subscriptions", h.unsubscribe)
	r.Post("/test", h.test)
}

func (h *Handler) vapidKey(w http.ResponseWriter, _ *http.Request) {
	httperr.JSON(w, http.StatusOK, map[string]string{"public_key": h.keys.PublicKey})
}

type subscribeBody struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

func (h *Handler) subscribe(w http.ResponseWriter, r *http.Request) {
	var in subscribeBody
	r.Body = http.MaxBytesReader(w, r.Body, httperr.JSONBodyCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_json", err.Error()))
		return
	}
	if !isValidPushEndpoint(in.Endpoint) {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_endpoint", "endpoint must be an absolute https URL"))
		return
	}
	sub, err := h.repo.Save(r.Context(), in.Endpoint, in.P256dh, in.Auth)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	httperr.JSON(w, http.StatusCreated, map[string]any{
		"id":         sub.ID,
		"created_at": sub.CreatedAt,
	})
}

type unsubscribeBody struct {
	Endpoint string `json:"endpoint"`
}

func (h *Handler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	var in unsubscribeBody
	r.Body = http.MaxBytesReader(w, r.Body, httperr.JSONBodyCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_json", err.Error()))
		return
	}
	if in.Endpoint == "" {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_endpoint", "endpoint is required"))
		return
	}
	if err := h.repo.DeleteByEndpoint(r.Context(), in.Endpoint); err != nil {
		httperr.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) test(w http.ResponseWriter, r *http.Request) {
	if h.sender == nil {
		httperr.Write(w, httperr.New(http.StatusServiceUnavailable, "push_disabled", "push sender not configured"))
		return
	}
	err := h.sender.Notify(r.Context(), Notification{
		LinkID: 0,
		Title:  "Foldex test notification",
		URL:    "/",
		Kind:   "test",
	})
	if err != nil {
		httperr.Write(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// isValidPushEndpoint rejects obviously invalid endpoints before we hit the
// DB. The real validation lives at SendNotification time (the Push service
// will 4xx us if the URL is malformed) — this is just defense in depth.
func isValidPushEndpoint(endpoint string) bool {
	if len(endpoint) < 10 || len(endpoint) > 2048 {
		return false
	}
	// Web Push endpoints are always https in practice (RFC 8030 says http
	// is allowed but no UA actually issues them).
	const httpsPrefix = "https://"
	if len(endpoint) < len(httpsPrefix) || endpoint[:len(httpsPrefix)] != httpsPrefix {
		return false
	}
	return true
}

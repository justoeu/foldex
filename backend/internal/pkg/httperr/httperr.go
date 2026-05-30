package httperr

import (
	"encoding/json"
	"errors"
	"net/http"
)

type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// cause is the underlying error, preserved for errors.Is / errors.As.
	// Never serialized — internal-only via Unwrap.
	cause error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.cause }

func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

var (
	ErrNotFound     = New(http.StatusNotFound, "not_found", "resource not found")
	ErrBadRequest   = New(http.StatusBadRequest, "bad_request", "invalid request")
	ErrConflict     = New(http.StatusConflict, "conflict", "resource conflict")
	ErrInternal     = New(http.StatusInternalServerError, "internal", "internal error")
	ErrUnauthorized = New(http.StatusUnauthorized, "unauthorized", "unauthorized")
)

// JSONBodyCap is the shared 64 KiB ceiling for plain JSON POST/PATCH bodies on
// links / folders / tags. ParseMultipartForm endpoints (image upload, backup)
// have their own larger caps. 64 KiB is generous — a Link payload with
// description + tags + slug is well under 4 KiB.
const JSONBodyCap = 64 << 10

// Wrap returns a *Error that preserves `cause` via Unwrap. Use when you have
// a domain typed error but also want the underlying cause to survive
// errors.Is / errors.As checks downstream (e.g. matching pgx.ErrNoRows). The
// envelope writer ignores the cause; only Status/Code/Message reach the wire.
func Wrap(status int, code, msg string, cause error) *Error {
	return &Error{Status: status, Code: code, Message: msg, cause: cause}
}

// envelope is the JSON shape used in responses: {"error": {...}}.
type envelope struct {
	Error *Error `json:"error"`
}

func Write(w http.ResponseWriter, err error) {
	var he *Error
	if !errors.As(err, &he) {
		he = ErrInternal
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(he.Status)
	_ = json.NewEncoder(w).Encode(envelope{Error: he})
}

func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

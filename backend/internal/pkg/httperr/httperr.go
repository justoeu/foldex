package httperr

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// DecodeJSON is a shared decode+cap+strict-parse helper for POST/PATCH
// handlers. Every handler that accepts a JSON body MUST go through a
// MaxBytesReader with JSONBodyCap; this function bakes that in so a new
// handler can't accidentally skip it.
func DecodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var in T
	r.Body = http.MaxBytesReader(w, r.Body, JSONBodyCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, New(http.StatusBadRequest, "invalid_json", err.Error())
	}
	return in, nil
}

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

// ParseID parses a chi URL param value as a positive int64. Returns a 400
// *Error when the value is missing, non-numeric, or <= 0. Takes the raw
// string (typically chi.URLParam(r, "id")) so this package stays router-
// agnostic — handlers already import chi, the pkg layer does not.
func ParseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, New(http.StatusBadRequest, "invalid_id", "id must be a positive integer")
	}
	return id, nil
}

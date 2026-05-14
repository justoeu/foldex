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
}

func (e *Error) Error() string { return e.Message }

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

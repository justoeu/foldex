package httperr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrite_KnownError(t *testing.T) {
	w := httptest.NewRecorder()
	Write(w, ErrNotFound)

	res := w.Result()
	defer res.Body.Close()
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
	assert.Equal(t, "application/json; charset=utf-8", res.Header.Get("Content-Type"))

	var body struct {
		Error Error `json:"error"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	assert.Equal(t, "not_found", body.Error.Code)
}

func TestWrite_GenericErrorBecomesInternal(t *testing.T) {
	w := httptest.NewRecorder()
	Write(w, errors.New("kaboom"))

	res := w.Result()
	defer res.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, res.StatusCode)
	var body struct {
		Error Error `json:"error"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	assert.Equal(t, "internal", body.Error.Code)
}

func TestNew_CustomError(t *testing.T) {
	e := New(http.StatusTeapot, "im_a_teapot", "short and stout")
	assert.Equal(t, http.StatusTeapot, e.Status)
	assert.Equal(t, "short and stout", e.Error())
}

func TestJSON_WritesPayload(t *testing.T) {
	w := httptest.NewRecorder()
	JSON(w, http.StatusCreated, map[string]string{"hello": "world"})

	res := w.Result()
	defer res.Body.Close()
	assert.Equal(t, http.StatusCreated, res.StatusCode)
	assert.Equal(t, "application/json; charset=utf-8", res.Header.Get("Content-Type"))
	var body map[string]string
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	assert.Equal(t, "world", body["hello"])
}

func TestJSON_NilBody(t *testing.T) {
	w := httptest.NewRecorder()
	JSON(w, http.StatusNoContent, nil)
	res := w.Result()
	defer res.Body.Close()
	assert.Equal(t, http.StatusNoContent, res.StatusCode)
}

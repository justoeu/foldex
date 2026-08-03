package httperr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestDecodeJSON_OK(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}`))
	w := httptest.NewRecorder()
	got, err := DecodeJSON[body](w, req)
	require.NoError(t, err)
	assert.Equal(t, "x", got.Name)
}

func TestDecodeJSON_InvalidJSON(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{not`))
	w := httptest.NewRecorder()
	_, err := DecodeJSON[body](w, req)
	require.Error(t, err)
	var he *Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "invalid_json", he.Code)
	assert.Equal(t, http.StatusBadRequest, he.Status)
}

func TestDecodeJSON_UnknownField(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x","extra":1}`))
	w := httptest.NewRecorder()
	_, err := DecodeJSON[body](w, req)
	require.Error(t, err)
	var he *Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "invalid_json", he.Code)
}

func TestDecodeJSONWithCap_TooLarge(t *testing.T) {
	type body struct {
		Data string `json:"data"`
	}
	// Cap of 8 bytes — body exceeds it.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"data":"0123456789"}`))
	w := httptest.NewRecorder()
	_, err := DecodeJSONWithCap[body](w, req, 8)
	require.Error(t, err)
	var he *Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "invalid_json", he.Code)
}

func TestWrap_UnwrapAndErrorsIs(t *testing.T) {
	cause := errors.New("root cause")
	e := Wrap(http.StatusConflict, "wrapped", "surface msg", cause)
	assert.Equal(t, "surface msg", e.Error())
	assert.Equal(t, cause, e.Unwrap())
	assert.True(t, errors.Is(e, cause))
	assert.Equal(t, http.StatusConflict, e.Status)
	assert.Equal(t, "wrapped", e.Code)
}

func TestParseID(t *testing.T) {
	id, err := ParseID("42")
	require.NoError(t, err)
	assert.Equal(t, int64(42), id)

	for _, raw := range []string{"", "abc", "0", "-3", "1.5"} {
		_, err := ParseID(raw)
		require.Error(t, err, "raw=%q", raw)
		var he *Error
		require.ErrorAs(t, err, &he)
		assert.Equal(t, "invalid_id", he.Code)
		assert.Equal(t, http.StatusBadRequest, he.Status)
	}
}

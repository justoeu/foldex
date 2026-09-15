package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

func TestWriteEmailFactorConfirmError(t *testing.T) {
	for name, tc := range map[string]struct {
		err      error
		handled  bool
		status   int
		contains string
	}{
		"nil":           {nil, false, 0, ""},
		"wrong code":    {ErrBadCredentials, true, http.StatusUnauthorized, `"code":"invalid_code"`},
		"no enrollment": {ErrNoPendingFactor, true, http.StatusBadRequest, `"code":"no_enrollment"`},
		"other":         {errors.New("boom"), false, 0, ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handled := writeEmailFactorConfirmError(rec, tc.err)
			assert.Equal(t, tc.handled, handled)
			if !tc.handled {
				assert.Equal(t, 200, rec.Code)
				return
			}
			require.Equal(t, tc.status, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.contains)
		})
	}
}

func TestWriteTOTPConfirmError(t *testing.T) {
	h := &Handler{cookies: CookieOptions{}, logger: slog.New(slog.DiscardHandler)}
	for name, tc := range map[string]struct {
		err      error
		handled  bool
		status   int
		contains string
	}{
		"nil":       {nil, false, 0, ""},
		"changed":   {ErrTOTPEnrollmentChanged, true, http.StatusConflict, `"code":"enrollment_changed"`},
		"challenge": {ErrChallengeInvalid, true, http.StatusUnauthorized, `"code":"challenge_invalid"`},
		"session":   {ErrSessionInvalid, true, http.StatusUnauthorized, `"code":"session_expired"`},
		"unknown":   {errors.New("boom"), true, http.StatusInternalServerError, `"code":"internal"`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handled := writeTOTPConfirmError(rec, h, tc.err)
			assert.Equal(t, tc.handled, handled)
			if !tc.handled {
				assert.Equal(t, 200, rec.Code)
				return
			}
			require.Equal(t, tc.status, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.contains)
		})
	}
}

func TestValidateUpdateUserInput(t *testing.T) {
	role := authctx.RoleOwner
	status := "nope"
	err := validateUpdateUserInput(updateUserInput{Role: &role})
	var he *httperr.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "invalid_role", he.Code)

	err = validateUpdateUserInput(updateUserInput{Status: &status})
	require.ErrorAs(t, err, &he)
	assert.Equal(t, "invalid_status", he.Code)

	okRole := authctx.RoleEditor
	okStatus := StatusActive
	require.NoError(t, validateUpdateUserInput(updateUserInput{Role: &okRole, Status: &okStatus}))
}

func TestWriteAdminUpdateUserError(t *testing.T) {
	h := &AdminHandler{logger: slog.New(slog.DiscardHandler)}
	for name, tc := range map[string]struct {
		err      error
		handled  bool
		status   int
		contains string
	}{
		"nil":     {nil, false, 0, ""},
		"missing": {ErrNoUser, true, http.StatusNotFound, `"code":"not_found"`},
		"last":    {ErrLastAdmin, true, http.StatusConflict, `"code":"last_admin"`},
		"owner":   {ErrOwnerImmutable, true, http.StatusConflict, `"code":"owner_immutable"`},
		"other":   {errors.New("boom"), true, http.StatusInternalServerError, `"code":"internal"`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handled := writeAdminUpdateUserError(rec, h, tc.err)
			assert.Equal(t, tc.handled, handled)
			if !tc.handled {
				return
			}
			require.Equal(t, tc.status, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.contains)
		})
	}
}

func TestEnrollmentSessionIssue(t *testing.T) {
	none, err := enrollmentSessionIssue(nil)
	require.NoError(t, err)
	assert.Empty(t, none.tokens.Access)

	issue, err := enrollmentSessionIssue(&PreAuth{TTL: SessionTTL{
		Access:  time.Minute,
		Refresh: time.Hour,
	}})
	require.NoError(t, err)
	assert.NotEmpty(t, issue.tokens.Access)
	assert.NotEmpty(t, issue.tokens.Refresh)
	assert.NotEmpty(t, issue.tokens.CSRF)
}

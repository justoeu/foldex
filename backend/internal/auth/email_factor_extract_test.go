package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

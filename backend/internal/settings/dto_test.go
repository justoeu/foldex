package settings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetMasterInput_Validate(t *testing.T) {
	require.NoError(t, setMasterInput{Password: "longenough"}.Validate())

	err := setMasterInput{Password: "short"}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 8 characters")

	// exactly the minimum is allowed.
	require.NoError(t, setMasterInput{Password: strings.Repeat("x", minMasterPasswordLen)}.Validate())
}

func TestSetMasterInput_HintValidation(t *testing.T) {
	// hint equal to password (case-insensitive) is rejected.
	same := "LongEnough1"
	err := setMasterInput{Password: "longenough1", Hint: &same}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be the same")

	// distinct hint is fine.
	ok := "rhymes with thunder"
	require.NoError(t, setMasterInput{Password: "longenough1", Hint: &ok}.Validate())

	// blank hint normalizes to nil.
	blank := "   "
	assert.Nil(t, setMasterInput{Password: "longenough1", Hint: &blank}.NormalizedHint())
}

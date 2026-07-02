package folders

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateInput_Normalize(t *testing.T) {
	in := CreateInput{Name: "  Trabalho  ", Color: "  "}
	in.Normalize()
	assert.Equal(t, "Trabalho", in.Name)
	assert.Equal(t, "#6366F1", in.Color, "empty color must default to indigo")
}

func TestCreateInput_Normalize_PreservesNonEmptyColor(t *testing.T) {
	in := CreateInput{Name: "x", Color: "  linear-gradient(135deg, #a, #b)  "}
	in.Normalize()
	assert.Equal(t, "linear-gradient(135deg, #a, #b)", in.Color)
}

func TestCreateInput_Validate(t *testing.T) {
	require.NoError(t, CreateInput{Name: "Docs", Color: "#abc"}.Validate())

	err := CreateInput{Name: ""}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	err = CreateInput{Name: strings.Repeat("a", 201)}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name too long")

	// Color validation now follows the cssvalid allowlist — long+invalid both
	// surface as "color must be ...".
	err = CreateInput{Name: "x", Color: strings.Repeat("a", 201)}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "color must be")

	// CSS injection vectors must be rejected.
	for _, hostile := range []string{
		`red url("https://evil/exfil")`,
		"expression(alert(1))",
		"linear-gradient(90deg, #abc, #def)",
	} {
		err := CreateInput{Name: "x", Color: hostile}.Validate()
		require.Error(t, err, "color %q must be refused", hostile)
		assert.Contains(t, err.Error(), "color must be")
	}
}

func TestUpdateInput_Empty(t *testing.T) {
	assert.True(t, UpdateInput{}.Empty())
	name := "x"
	assert.False(t, UpdateInput{Name: &name}.Empty())
}

func TestUpdateInput_Normalize(t *testing.T) {
	rawName := "  Docs  "
	rawColor := "  #abc  "
	in := UpdateInput{Name: &rawName, Color: &rawColor}
	in.Normalize()
	assert.Equal(t, "Docs", *in.Name)
	assert.Equal(t, "#abc", *in.Color)
}

func TestUpdateInput_Validate(t *testing.T) {
	require.NoError(t, UpdateInput{}.Validate())

	valid := "Docs"
	require.NoError(t, UpdateInput{Name: &valid}.Validate())

	empty := ""
	err := UpdateInput{Name: &empty}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	long := strings.Repeat("a", 201)
	err = UpdateInput{Name: &long}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name too long")

	longColor := strings.Repeat("a", 201)
	err = UpdateInput{Color: &longColor}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "color must be")
}

func TestCreateInput_Validate_Password(t *testing.T) {
	require.NoError(t, CreateInput{Name: "Docs", Color: "#abc"}.Validate(), "no password is valid — folder just isn't protected")

	ok := "1234"
	require.NoError(t, CreateInput{Name: "Docs", Color: "#abc", Password: &ok}.Validate())

	short := "abc"
	err := CreateInput{Name: "Docs", Color: "#abc", Password: &short}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 4 characters")
}

func TestUpdateInput_UnmarshalJSON_PasswordTriState(t *testing.T) {
	// Absent → unchanged.
	var absent UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"name":"x"}`), &absent))
	assert.False(t, absent.PasswordSet)
	assert.Nil(t, absent.Password)

	// Explicit null → remove protection.
	var removed UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"password":null,"current_password":"old"}`), &removed))
	assert.True(t, removed.PasswordSet)
	assert.Nil(t, removed.Password)
	require.NotNil(t, removed.CurrentPassword)
	assert.Equal(t, "old", *removed.CurrentPassword)

	// String value → set/replace.
	var set UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"password":"new-pass"}`), &set))
	assert.True(t, set.PasswordSet)
	require.NotNil(t, set.Password)
	assert.Equal(t, "new-pass", *set.Password)
}

func TestUpdateInput_Empty_PasswordSet(t *testing.T) {
	assert.True(t, UpdateInput{}.Empty())
	var withPassword UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"password":null}`), &withPassword))
	assert.False(t, withPassword.Empty(), "an explicit password change/removal is not an empty update even with no other fields")
}

func TestUpdateInput_Validate_Password(t *testing.T) {
	var setShort UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"password":"abc"}`), &setShort))
	err := setShort.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 4 characters")

	var removed UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"password":null}`), &removed))
	require.NoError(t, removed.Validate(), "removing a password (null) never needs the length check")
}

// ── password_hint (ADR-29) ────────────────────────────────────────────────

func TestCreateInput_Normalize_Hint(t *testing.T) {
	pw := "secret"
	hint := "  my dog's name  "
	in := CreateInput{Name: "x", Password: &pw, PasswordHint: &hint}
	in.Normalize()
	require.NotNil(t, in.PasswordHint)
	assert.Equal(t, "my dog's name", *in.PasswordHint, "hint is trimmed")

	blank := "   "
	in2 := CreateInput{Name: "x", PasswordHint: &blank}
	in2.Normalize()
	assert.Nil(t, in2.PasswordHint, "blank hint collapses to nil")
}

func TestCreateInput_Validate_Hint(t *testing.T) {
	pw := "correct-horse"

	// hint equal to the password (case-insensitive) is rejected.
	same := "Correct-Horse"
	err := CreateInput{Name: "x", Color: "#abc", Password: &pw, PasswordHint: &same}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be the same")

	// distinct hint is fine.
	ok := "rhymes with force"
	require.NoError(t, CreateInput{Name: "x", Color: "#abc", Password: &pw, PasswordHint: &ok}.Validate())

	// over-long hint is rejected.
	long := strings.Repeat("a", maxPasswordHintLen+1)
	err = CreateInput{Name: "x", Color: "#abc", Password: &pw, PasswordHint: &long}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too long")
}

func TestUpdateInput_Unmarshal_HintTristate(t *testing.T) {
	var absent UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"name":"x"}`), &absent))
	assert.False(t, absent.PasswordHintSet)

	var cleared UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"password_hint":null}`), &cleared))
	assert.True(t, cleared.PasswordHintSet)
	assert.Nil(t, cleared.PasswordHint)
	assert.False(t, cleared.Empty(), "an explicit hint change is not an empty update")

	var set UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"password_hint":"a clue"}`), &set))
	assert.True(t, set.PasswordHintSet)
	require.NotNil(t, set.PasswordHint)
	assert.Equal(t, "a clue", *set.PasswordHint)
}

func TestUpdateInput_Validate_HintEqualsNewPassword(t *testing.T) {
	var in UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"password":"hunter2","password_hint":"hunter2"}`), &in))
	in.Normalize()
	err := in.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be the same")
}

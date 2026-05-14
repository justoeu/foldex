package folders

import (
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

	err = CreateInput{Name: "x", Color: strings.Repeat("a", 201)}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "color too long")
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
	assert.Contains(t, err.Error(), "color too long")
}

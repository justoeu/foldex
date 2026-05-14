package tags

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateInput_Normalize(t *testing.T) {
	in := CreateInput{Name: "  Jira  ", Color: "  "}
	in.Normalize()
	assert.Equal(t, "Jira", in.Name)
	assert.Equal(t, "#6366F1", in.Color, "empty color must default")
}

func TestCreateInput_Validate(t *testing.T) {
	require.NoError(t, CreateInput{Name: "Docs"}.Validate())

	err := CreateInput{Name: ""}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	long := make([]byte, 81)
	for i := range long {
		long[i] = 'a'
	}
	err = CreateInput{Name: string(long)}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too long")
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
	// nil name → no error (partial update)
	require.NoError(t, UpdateInput{}.Validate())

	validName := "Docs"
	require.NoError(t, UpdateInput{Name: &validName}.Validate())

	emptyName := ""
	err := UpdateInput{Name: &emptyName}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	longName := make([]byte, 81)
	for i := range longName {
		longName[i] = 'a'
	}
	s := string(longName)
	err = UpdateInput{Name: &s}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too long")
}

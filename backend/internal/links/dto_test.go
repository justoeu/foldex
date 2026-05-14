package links

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FolderID is encoded with a custom UnmarshalJSON that distinguishes
// "field absent" (do not touch) from "field is null" (clear). These tests
// pin the contract: absent → FolderIDSet=false; null → FolderIDSet=true,
// FolderID=nil; number → FolderIDSet=true, FolderID points at the number.

func TestUpdateInput_FolderID_AbsentMeansDoNotTouch(t *testing.T) {
	var u UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"title": "x"}`), &u))
	assert.False(t, u.FolderIDSet)
	assert.Nil(t, u.FolderID)
}

func TestUpdateInput_FolderID_NullMeansClear(t *testing.T) {
	var u UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"folder_id": null}`), &u))
	assert.True(t, u.FolderIDSet)
	assert.Nil(t, u.FolderID)
}

func TestUpdateInput_FolderID_NumberMeansAssign(t *testing.T) {
	var u UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"folder_id": 42}`), &u))
	assert.True(t, u.FolderIDSet)
	require.NotNil(t, u.FolderID)
	assert.Equal(t, int64(42), *u.FolderID)
}

func TestUpdateInput_FolderID_InvalidShapeErrors(t *testing.T) {
	var u UpdateInput
	require.Error(t, json.Unmarshal([]byte(`{"folder_id": "not-a-number"}`), &u))
}

func TestCreateInput_NormalizeFillsTitleFromURL(t *testing.T) {
	in := CreateInput{URL: "  https://x  ", Title: "  "}
	in.Normalize()
	assert.Equal(t, "https://x", in.URL)
	assert.Equal(t, "https://x", in.Title, "empty title must fall back to URL")
}

func TestCreateInput_Validate(t *testing.T) {
	require.NoError(t, CreateInput{URL: "https://example.com", Title: "x"}.Validate())

	cases := []struct {
		name string
		in   CreateInput
		msg  string
	}{
		{"no url", CreateInput{}, "url is required"},
		{"non-http scheme", CreateInput{URL: "ftp://example.com"}, "scheme must be http"},
		{"relative path", CreateInput{URL: "/x"}, "absolute http"},
		{"title too long", CreateInput{URL: "https://x", Title: longString(501)}, "title too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}

func TestUpdateInput_Normalize(t *testing.T) {
	rawURL := "  https://example.com  "
	rawTitle := "  My Link  "
	in := UpdateInput{URL: &rawURL, Title: &rawTitle}
	in.Normalize()
	assert.Equal(t, "https://example.com", *in.URL)
	assert.Equal(t, "My Link", *in.Title)
}

func TestUpdateInput_Validate(t *testing.T) {
	// nil fields → no error (partial update)
	require.NoError(t, UpdateInput{}.Validate())

	validURL := "https://example.com"
	validTitle := "hello"
	require.NoError(t, UpdateInput{URL: &validURL, Title: &validTitle}.Validate())

	cases := []struct {
		name string
		in   UpdateInput
		msg  string
	}{
		{"empty url", UpdateInput{URL: ptrS("")}, "url is required"},
		{"ftp scheme", UpdateInput{URL: ptrS("ftp://x.com")}, "scheme must be http"},
		{"relative url", UpdateInput{URL: ptrS("/path")}, "absolute http"},
		{"empty title", UpdateInput{URL: &validURL, Title: ptrS("")}, "title is required"},
		{"title too long", UpdateInput{URL: &validURL, Title: ptrS(longString(501))}, "title too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}

func ptrS(s string) *string { return &s }

func longString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

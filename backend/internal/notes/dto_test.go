package notes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateInput_Normalize_SanitizesBodyHTML(t *testing.T) {
	c := CreateInput{Title: "  Hi  ", BodyHTML: `<p>ok</p><script>alert(1)</script>`}
	c.Normalize()
	assert.Equal(t, "Hi", c.Title)
	assert.NotContains(t, c.BodyHTML, "<script")
	assert.Contains(t, c.BodyHTML, "<p>ok</p>")
}

func TestCreateInput_Validate_RequiresTitle(t *testing.T) {
	c := CreateInput{Title: "", BodyHTML: "<p>x</p>"}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "title")
}

func TestCreateInput_Validate_TitleTooLong(t *testing.T) {
	c := CreateInput{Title: strings.Repeat("a", MaxTitleBytes+1), BodyHTML: ""}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "title too long")
}

func TestCreateInput_Validate_BodyTooLongAfterSanitize(t *testing.T) {
	c := CreateInput{Title: "ok", BodyHTML: "<p>" + strings.Repeat("a", MaxBodyHTMLBytes) + "</p>"}
	c.Normalize() // sanitize runs here — length check is post-sanitize
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "body too long")
}

func TestCreateInput_Validate_InvalidSlug(t *testing.T) {
	bad := "Not A Slug!"
	c := CreateInput{Title: "ok", Slug: &bad}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slug")
}

func TestCreateInput_Validate_NumericSlugRejected(t *testing.T) {
	bad := "12345"
	c := CreateInput{Title: "ok", Slug: &bad}
	err := c.Validate()
	require.Error(t, err, "purely numeric slugs must be rejected — they'd shadow /n/{id}")
}

func TestUpdateInput_Validate_Passthrough(t *testing.T) {
	title := "ok"
	u := UpdateInput{Title: &title}
	assert.NoError(t, u.Validate())
}

func TestUpdateInput_Validate_EmptyTitleRejected(t *testing.T) {
	empty := ""
	u := UpdateInput{Title: &empty}
	err := u.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "title is required")
}

func TestUpdateInput_Validate_TitleTooLong(t *testing.T) {
	long := strings.Repeat("a", MaxTitleBytes+1)
	u := UpdateInput{Title: &long}
	err := u.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "title too long")
}

func TestUpdateInput_Validate_BodyTooLong(t *testing.T) {
	long := strings.Repeat("a", MaxBodyHTMLBytes+1)
	u := UpdateInput{BodyHTML: &long}
	err := u.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "body too long")
}

func TestUpdateInput_Validate_InvalidSlugWhenSet(t *testing.T) {
	bad := "Not A Slug!"
	u := UpdateInput{Slug: &bad, SlugSet: true}
	err := u.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slug")
}

func TestUpdateInput_Validate_NullSlugSkipsFormatCheck(t *testing.T) {
	// SlugSet=true, Slug=nil means "regenerate from title" — Validate must
	// not try to format-check a nil slug.
	u := UpdateInput{SlugSet: true}
	assert.NoError(t, u.Validate())
}

// TestUpdateInput_FolderID_AbsentVsNullVsValue locks the tri-state JSON
// decoding pattern shared with links.UpdateInput.
func TestUpdateInput_FolderID_AbsentVsNullVsValue(t *testing.T) {
	var u UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"title":"x"}`), &u))
	assert.False(t, u.FolderIDSet, "absent field must not set FolderIDSet")

	var u2 UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"folder_id":null}`), &u2))
	assert.True(t, u2.FolderIDSet)
	assert.Nil(t, u2.FolderID, "explicit null means clear")

	var u3 UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"folder_id":42}`), &u3))
	assert.True(t, u3.FolderIDSet)
	require.NotNil(t, u3.FolderID)
	assert.EqualValues(t, 42, *u3.FolderID)
}

func TestUpdateInput_Slug_AbsentVsNullVsValue(t *testing.T) {
	var u UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{}`), &u))
	assert.False(t, u.SlugSet)

	var u2 UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"slug":null}`), &u2))
	assert.True(t, u2.SlugSet)
	assert.Nil(t, u2.Slug, "null means regenerate from title")

	var u3 UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"slug":"my-slug"}`), &u3))
	assert.True(t, u3.SlugSet)
	require.NotNil(t, u3.Slug)
	assert.Equal(t, "my-slug", *u3.Slug)
}

// TestUpdateInput_BodyHTML_NilVsEmptyString locks that BodyHTML is a plain
// pointer (not tri-state) — nil means untouched, an explicit empty string is
// a legal cleared body.
func TestUpdateInput_BodyHTML_NilVsEmptyString(t *testing.T) {
	var u UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{}`), &u))
	assert.Nil(t, u.BodyHTML)

	var u2 UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"body_html":""}`), &u2))
	require.NotNil(t, u2.BodyHTML)
	assert.Equal(t, "", *u2.BodyHTML)
}

func TestUpdateInput_Normalize_SanitizesBodyHTML(t *testing.T) {
	body := `<p>ok</p><script>alert(1)</script>`
	u := UpdateInput{BodyHTML: &body}
	u.Normalize()
	require.NotNil(t, u.BodyHTML)
	assert.NotContains(t, *u.BodyHTML, "<script")
}

// TestUpdateInput_DoesNotAcceptBodyTextField confirms body_text is never a
// client-settable field — it's always server-derived from the sanitized
// body_html, so search can't drift from what's stored. Note: UpdateInput
// implements json.Unmarshaler (for the tri-state fields), and the standard
// library hands decoding entirely to that method — DisallowUnknownFields on
// the outer decoder has no effect for types with a custom UnmarshalJSON, the
// same as links.UpdateInput already behaves. The actual guarantee here is
// structural: BodyText has no json tag on UpdateInput, so a "body_text" key
// is silently dropped rather than landing anywhere — decoding must still
// succeed and BodyHTML must stay untouched.
func TestUpdateInput_DoesNotAcceptBodyTextField(t *testing.T) {
	var u UpdateInput
	dec := json.NewDecoder(strings.NewReader(`{"body_text":"poisoned"}`))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&u))
	assert.Nil(t, u.BodyHTML, "an unrecognized body_text key must not populate BodyHTML or any other field")
}

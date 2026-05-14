package importer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleJSON = `{
  "version": 1,
  "exported_at": "2026-05-11T10:30:00Z",
  "tags": [
    {"name": "jira", "color": "#1f6feb", "icon": "🪲"}
  ],
  "links": [
    {
      "url": "https://jira.example/INV-1",
      "title": "INV-1",
      "tags": ["jira"],
      "click_count": 7,
      "created_at": "2026-05-01T12:00:00Z"
    }
  ]
}`

func TestParseJSON(t *testing.T) {
	f, err := ParseJSON(strings.NewReader(sampleJSON))
	require.NoError(t, err)
	assert.Equal(t, 1, f.Version)
	require.Len(t, f.Tags, 1)
	assert.Equal(t, "jira", f.Tags[0].Name)
	require.NotNil(t, f.Tags[0].Icon)
	assert.Equal(t, "🪲", *f.Tags[0].Icon)
	require.Len(t, f.Links, 1)
	assert.Equal(t, "INV-1", f.Links[0].Title)
	assert.Equal(t, int64(7), f.Links[0].ClickCount)
}

func TestParseJSON_Empty(t *testing.T) {
	f, err := ParseJSON(strings.NewReader(`{"version":1,"tags":[],"links":[]}`))
	require.NoError(t, err)
	assert.Equal(t, 1, f.Version)
	assert.Empty(t, f.Tags)
	assert.Empty(t, f.Links)
}

func TestParseJSON_Malformed(t *testing.T) {
	_, err := ParseJSON(strings.NewReader(`{`))
	require.Error(t, err)
}

func TestValidate_HappyPath(t *testing.T) {
	f, err := ParseJSON(strings.NewReader(sampleJSON))
	require.NoError(t, err)
	require.NoError(t, f.Validate())
}

func TestValidate_WrongVersion(t *testing.T) {
	// Version 1 and 2 are both supported (1 = pre-folders, 2 = with folders).
	// Anything else must be rejected.
	f := JSONFile{Version: 99, Tags: []JSONTag{}, Links: []JSONLink{}}
	require.Error(t, f.Validate())
}

func TestValidate_AcceptsBothV1AndV2(t *testing.T) {
	require.NoError(t, JSONFile{Version: 1, Tags: []JSONTag{}, Links: []JSONLink{}}.Validate())
	require.NoError(t, JSONFile{Version: 2, Tags: []JSONTag{}, Folders: []JSONFolder{}, Links: []JSONLink{}}.Validate())
}

func TestValidate_FolderNameEmpty(t *testing.T) {
	f := JSONFile{
		Version: 2,
		Folders: []JSONFolder{{Name: "  ", Color: "#fff"}},
	}
	require.Error(t, f.Validate())
}

func TestValidate_TagNameEmpty(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{{Name: "  ", Color: "#fff"}},
		Links:   []JSONLink{},
	}
	require.Error(t, f.Validate())
}

func TestValidate_TagNameTooLong(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{{Name: strings.Repeat("x", 81), Color: "#fff"}},
		Links:   []JSONLink{},
	}
	require.Error(t, f.Validate())
}

func TestValidate_LinkURLEmpty(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{},
		Links:   []JSONLink{{URL: "", Title: "t"}},
	}
	require.Error(t, f.Validate())
}

func TestValidate_LinkURLRelative(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{},
		Links:   []JSONLink{{URL: "/relative/path", Title: "t"}},
	}
	require.Error(t, f.Validate())
}

func TestValidate_LinkURLBadScheme(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{},
		Links:   []JSONLink{{URL: "ftp://example.com", Title: "t"}},
	}
	require.Error(t, f.Validate())
}

func TestValidate_LinkTitleTooLong(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{},
		Links:   []JSONLink{{URL: "https://example.com", Title: strings.Repeat("x", 501)}},
	}
	require.Error(t, f.Validate())
}

func TestValidate_LinkTagNameEmpty(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{},
		Links:   []JSONLink{{URL: "https://example.com", Title: "t", Tags: []string{"  "}}},
	}
	require.Error(t, f.Validate())
}

func TestValidate_LinkTagNameTooLong(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{},
		Links:   []JSONLink{{URL: "https://example.com", Title: "t", Tags: []string{strings.Repeat("x", 81)}}},
	}
	require.Error(t, f.Validate())
}

func TestValidate_LinkCreatedAtInvalid(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{},
		Links:   []JSONLink{{URL: "https://example.com", Title: "t", CreatedAt: "not-a-date"}},
	}
	require.Error(t, f.Validate())
}

func TestValidate_LinkCreatedAtAbsent(t *testing.T) {
	f := JSONFile{
		Version: 1,
		Tags:    []JSONTag{},
		Links:   []JSONLink{{URL: "https://example.com", Title: "t"}},
	}
	require.NoError(t, f.Validate())
}

package importer

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleNetscape = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
  <DT><A HREF="https://example.com" ADD_DATE="1000">Example</A>
  <DT><H3>Jira</H3>
  <DL><p>
    <DT><A HREF="https://jira.example/INV-1">INV-1</A>
    <DT><A HREF="https://jira.example/INV-2">INV-2</A>
    <DT><H3>Sprints</H3>
    <DL><p>
      <DT><A HREF="https://jira.example/sprint/42">Sprint 42</A>
    </DL><p>
  </DL><p>
  <DT><A HREF="https://docs.example/guide">Guide</A>
</DL><p>
`

func netscapeLinks(count int) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE NETSCAPE-Bookmark-file-1><DL><p>`)
	for i := 0; i < count; i++ {
		b.WriteString(`<DT><A HREF="https://example.com/`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`">t</A>`)
	}
	b.WriteString(`</DL><p>`)
	return b.String()
}

func TestParseNetscape_ItemLimitBoundary(t *testing.T) {
	items, err := ParseNetscape(strings.NewReader(netscapeLinks(maxImportItems)))
	require.NoError(t, err)
	assert.Len(t, items, maxImportItems)

	items, err = ParseNetscape(strings.NewReader(netscapeLinks(maxImportItems + 1)))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTooManyItems)
	assert.Nil(t, items)
}

func TestParseNetscape_FlatAndNested(t *testing.T) {
	items, err := ParseNetscape(strings.NewReader(sampleNetscape))
	require.NoError(t, err)
	require.Len(t, items, 5)

	// 1. top-level link → no folder, no tags
	assert.Equal(t, "https://example.com", items[0].URL)
	assert.Equal(t, "Example", items[0].Title)
	assert.Empty(t, items[0].Tags)
	assert.Nil(t, items[0].Folder)

	// 2. inside <H3>Jira</H3> → folder=Jira, no extra tags
	assert.Equal(t, "https://jira.example/INV-1", items[1].URL)
	require.NotNil(t, items[1].Folder)
	assert.Equal(t, "Jira", *items[1].Folder)
	assert.Empty(t, items[1].Tags)

	// 3. inside Jira > Sprints → deepest H3 is folder, outer H3s become tags
	assert.Equal(t, "https://jira.example/sprint/42", items[3].URL)
	require.NotNil(t, items[3].Folder)
	assert.Equal(t, "Sprints", *items[3].Folder)
	assert.Equal(t, []string{"Jira"}, items[3].Tags)

	// 4. last top-level link, popped back out of Jira → no folder, no tags
	assert.Equal(t, "https://docs.example/guide", items[4].URL)
	assert.Empty(t, items[4].Tags)
	assert.Nil(t, items[4].Folder)
}

func TestParseNetscape_EmptyInput(t *testing.T) {
	items, err := ParseNetscape(strings.NewReader(""))
	require.NoError(t, err)
	assert.Empty(t, items)
}

// Empty <H3></H3> must not desync the folder stack: previously we skipped
// pushing blank names but still popped on </DL>, so a sibling after an empty
// folder lost its parent scope.
func TestParseNetscape_EmptyH3KeepsParentFolder(t *testing.T) {
	body := `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<DL><p>
  <DT><H3>Parent</H3>
  <DL><p>
    <DT><H3></H3>
    <DL><p>
      <DT><A HREF="https://inside-empty.example">Inside empty</A>
    </DL><p>
    <DT><A HREF="https://sibling.example">Sibling after empty</A>
  </DL><p>
  <DT><A HREF="https://root.example">Root again</A>
</DL><p>
`
	items, err := ParseNetscape(strings.NewReader(body))
	require.NoError(t, err)
	require.Len(t, items, 3)

	// Link nested under blank H3 still inherits Parent (blank is not a folder).
	assert.Equal(t, "https://inside-empty.example", items[0].URL)
	require.NotNil(t, items[0].Folder)
	assert.Equal(t, "Parent", *items[0].Folder)

	// Sibling after the empty folder's </DL> must still be under Parent.
	assert.Equal(t, "https://sibling.example", items[1].URL)
	require.NotNil(t, items[1].Folder)
	assert.Equal(t, "Parent", *items[1].Folder)
	assert.Empty(t, items[1].Tags)

	// After Parent closes, root again.
	assert.Equal(t, "https://root.example", items[2].URL)
	assert.Nil(t, items[2].Folder)
}

func TestParseNetscape_ToleratesMalformedCloseOrder(t *testing.T) {
	body := `<H3>Crossed<DL></H3>
		<A HREF="https://nested.example">Nested</A>
	</DL></DL>
	<A HREF="https://root.example">Root</A>`

	items, err := ParseNetscape(strings.NewReader(body))
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.NotNil(t, items[0].Folder)
	assert.Equal(t, "Crossed", *items[0].Folder)
	assert.Nil(t, items[1].Folder, "surplus closing tags must not underflow the folder stack")
}

func TestParseNetscape_DeepNestingAndFullUnwind(t *testing.T) {
	const depth = 64
	var body strings.Builder
	wantTags := make([]string, 0, depth-1)
	for i := 0; i < depth; i++ {
		name := "Level " + strconv.Itoa(i)
		body.WriteString("<H3>" + name + "</H3><DL>")
		if i < depth-1 {
			wantTags = append(wantTags, name)
		}
	}
	body.WriteString(`<A HREF="https://deep.example">Deep</A>`)
	body.WriteString(strings.Repeat("</DL>", depth))
	body.WriteString(`<A HREF="https://root.example">Root</A>`)

	items, err := ParseNetscape(strings.NewReader(body.String()))
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.NotNil(t, items[0].Folder)
	assert.Equal(t, "Level 63", *items[0].Folder)
	assert.Equal(t, wantTags, items[0].Tags)
	assert.Nil(t, items[1].Folder)
	assert.Empty(t, items[1].Tags)
}

func TestParseNetscape_BlankNamesHrefTitleAndDescription(t *testing.T) {
	body := `<H3>
	</H3><DL>
		<A HREF="   ">Skipped</A>
		<A HREF="https://fallback.example">
		</A><DD>
	</DL>`

	items, err := ParseNetscape(strings.NewReader(body))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "https://fallback.example", items[0].Title)
	assert.Nil(t, items[0].Folder)
	assert.Empty(t, items[0].Tags)
	assert.Nil(t, items[0].Description)
}

var errInjectedReader = errors.New("injected reader failure")

type failingReader struct {
	content *strings.Reader
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.content.Len() == 0 {
		return 0, errInjectedReader
	}
	return r.content.Read(p)
}

func TestParseNetscape_ReturnsTokenizerErrorWithPartialItems(t *testing.T) {
	r := &failingReader{content: strings.NewReader(`<A HREF="https://kept.example">Kept</A>`)}

	items, err := ParseNetscape(r)
	require.Error(t, err)
	assert.ErrorIs(t, err, errInjectedReader)
	require.Len(t, items, 1)
	assert.Equal(t, "https://kept.example", items[0].URL)
}

func TestParseNetscape_LinkWithoutTitleFallsBackToURL(t *testing.T) {
	body := `<DL><DT><A HREF="https://x"></A></DL>`
	items, err := ParseNetscape(strings.NewReader(body))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "https://x", items[0].Title)
}

func TestParseNetscape_LinkWithoutHrefSkipped(t *testing.T) {
	body := `<DL><DT><A>Not a link</A><DT><A HREF="https://kept">Kept</A></DL>`
	items, err := ParseNetscape(strings.NewReader(body))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "https://kept", items[0].URL)
}

func TestParseNetscape_RejectsDangerousSchemes(t *testing.T) {
	// Locks security invariant: a Netscape file is user-supplied and must
	// never funnel non-http(s) URLs into link.url. Anything that lands in
	// the DB is later rendered as <a href={url}> (LinkDialog) and fed to
	// the screenshot endpoint via /go/{id} resolution — file:// + the
	// manual screenshot trigger would be read-anywhere.
	body := `<DL>
		<DT><A HREF="javascript:alert(1)">JS</A>
		<DT><A HREF="data:text/html,evil">Data</A>
		<DT><A HREF="file:///etc/passwd">File</A>
		<DT><A HREF="vbscript:msgbox">VB</A>
		<DT><A HREF="mailto:x@y.z">Mail</A>
		<DT><A HREF="tel:+5511">Tel</A>
		<DT><A HREF="ftp://old.example/x">FTP</A>
		<DT><A HREF="https://kept.example">Kept</A>
		<DT><A HREF="HTTP://CAPS.example">Caps</A>
	</DL>`
	items, err := ParseNetscape(strings.NewReader(body))
	require.NoError(t, err)
	require.Len(t, items, 2, "only http and https survive scheme filter")
	assert.Equal(t, "https://kept.example", items[0].URL)
	assert.Equal(t, "HTTP://CAPS.example", items[1].URL, "uppercase HTTP must also be accepted")
}

func TestIsHTTPScheme(t *testing.T) {
	cases := []struct {
		href string
		ok   bool
	}{
		{"https://example.com", true},
		{"http://example.com", true},
		{"HTTPS://EXAMPLE.com", true},
		{"  https://example.com  ", true},
		{"javascript:alert(1)", false},
		{"data:text/html,x", false},
		{"file:///etc/passwd", false},
		{"vbscript:x", false},
		{"mailto:a@b.c", false},
		{"tel:+1", false},
		{"ftp://x", false},
		{"//example.com", false},
		{"example.com", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.href, func(t *testing.T) {
			assert.Equal(t, tc.ok, isHTTPScheme(tc.href))
		})
	}
}

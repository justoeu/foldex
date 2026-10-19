package changecheck

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// tokenizerAtLink returns a tokenizer positioned on the first <link> tag of
// doc, which is the state attrValue's caller (feedIDCollector.onStart) is in.
func tokenizerAtLink(t *testing.T, doc string) *html.Tokenizer {
	t.Helper()
	z := html.NewTokenizer(bytes.NewReader([]byte(doc)))
	for {
		tt := z.Next()
		require.NotEqual(t, html.ErrorToken, tt, "no <link> in %q", doc)
		if tt == html.StartTagToken || tt == html.SelfClosingTagToken {
			name, _ := z.TagName()
			if string(name) == "link" {
				return z
			}
		}
	}
}

func TestAttrValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		doc  string
		key  string
		want string
	}{
		{name: "only attribute", doc: `<link href="https://x.test/a">`, key: "href", want: "https://x.test/a"},
		{name: "first of many", doc: `<link href="https://x.test/a" rel="alternate" type="text/html">`, key: "href", want: "https://x.test/a"},
		{name: "last of many", doc: `<link rel="alternate" type="text/html" href="https://x.test/b">`, key: "href", want: "https://x.test/b"},
		{name: "missing key", doc: `<link rel="alternate" type="text/html">`, key: "href", want: ""},
		{name: "uppercase attribute name", doc: `<link HREF="https://x.test/c">`, key: "href", want: "https://x.test/c"},
		{name: "empty value", doc: `<link href="" rel="alternate">`, key: "href", want: ""},
		{name: "duplicate attribute tokenizer keeps first", doc: `<link href="https://x.test/first" href="https://x.test/second">`, key: "href", want: "https://x.test/first"},
		{name: "non-href key", doc: `<link href="https://x.test/d" rel="alternate">`, key: "rel", want: "alternate"},
		{name: "self-closing tag", doc: `<link href="https://x.test/e"/>`, key: "href", want: "https://x.test/e"},
		{name: "valueless attribute before target", doc: `<link disabled href="https://x.test/f">`, key: "href", want: "https://x.test/f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, attrValue(tokenizerAtLink(t, tc.doc), tc.key))
		})
	}
}

// The same branches exercised end-to-end through extractFeedItemIDs, whose
// <link href> path inside <entry>/<item> is attrValue's only production use.
func TestExtractFeedItemIDs_LinkHref(t *testing.T) {
	t.Parallel()
	atom := `<feed xmlns="http://www.w3.org/2005/Atom">
		<entry><link rel="alternate" href="https://x.test/posts/1"/></entry>
		<entry><link href="https://x.test/posts/2" rel="self"/></entry>
		<entry><link rel="alternate"/></entry>
	</feed>`
	assert.ElementsMatch(t,
		[]string{"https://x.test/posts/1", "https://x.test/posts/2"},
		extractFeedItemIDs([]byte(atom)),
		"entry links without href contribute nothing")

	outsideItem := `<feed><link href="https://x.test/feed-level"/><entry><id>urn:1</id></entry></feed>`
	assert.Equal(t, []string{"urn:1"}, extractFeedItemIDs([]byte(outsideItem)),
		"a feed-level link outside any entry is not an item id")
}

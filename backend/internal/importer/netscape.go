package importer

import (
	"io"
	"strings"

	"golang.org/x/net/html"
)

type Item struct {
	URL    string
	Title  string
	Tags   []string
	Folder *string // innermost H3 in scope; nil when the link is at root
}

// ParseNetscape walks a Netscape Bookmark HTML file and returns one Item per
// <A> link. Each <H3> defines a folder scope. The innermost (deepest) H3
// becomes the link's folder; the outer H3s above it become tags applied to
// the link (so a Chrome export's "Bookmarks Bar / Work / Issues" maps to
// folder=Issues with tags=[Bookmarks Bar, Work]). Foldex folders are flat
// (1-level), so nesting collapses to "deepest wins".
func ParseNetscape(r io.Reader) ([]Item, error) {
	z := html.NewTokenizer(r)
	var (
		items []Item
		// stack of tag names from H3 headers; pushed at H3, popped at </DL>
		tagStack []string
		// pendingTag captures the latest H3 text until we hit a closing tag
		captureTag bool
		pendingTag string
	)
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			if err := z.Err(); err == io.EOF {
				return items, nil
			} else {
				return items, err
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch strings.ToLower(t.Data) {
			case "dl":
				// New folder scope; we don't push here because the H3 that named
				// it has already been pushed to tagStack at start tag.
			case "h3":
				captureTag = true
				pendingTag = ""
			case "a":
				href := attr(t, "href")
				if href == "" {
					continue
				}
				title := readText(z)
				it := Item{URL: href, Title: strings.TrimSpace(title)}
				if it.Title == "" {
					it.Title = href
				}
				if len(tagStack) > 0 {
					// Deepest H3 = folder. Outer H3s above it = tags.
					f := tagStack[len(tagStack)-1]
					it.Folder = &f
					if len(tagStack) > 1 {
						it.Tags = append(it.Tags, tagStack[:len(tagStack)-1]...)
					}
				}
				items = append(items, it)
			}
		case html.TextToken:
			if captureTag {
				pendingTag += z.Token().Data
			}
		case html.EndTagToken:
			t := z.Token()
			switch strings.ToLower(t.Data) {
			case "h3":
				if captureTag {
					name := strings.TrimSpace(pendingTag)
					if name != "" {
						tagStack = append(tagStack, name)
					}
					captureTag = false
					pendingTag = ""
				}
			case "dl":
				if len(tagStack) > 0 {
					tagStack = tagStack[:len(tagStack)-1]
				}
			}
		}
	}
}

// readText consumes the next text token (the body of a tag like <A>title</A>).
func readText(z *html.Tokenizer) string {
	if z.Next() == html.TextToken {
		return z.Token().Data
	}
	return ""
}

func attr(t html.Token, key string) string {
	key = strings.ToLower(key)
	for _, a := range t.Attr {
		if strings.ToLower(a.Key) == key {
			return a.Val
		}
	}
	return ""
}

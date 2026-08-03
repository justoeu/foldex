package importer

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ErrTooManyItems is returned when a Netscape/JSON import exceeds maxImportItems.
var ErrTooManyItems = errors.New("import exceeds maximum item count")

type Item struct {
	URL         string
	Title       string
	Tags        []string
	Folder      *string // innermost H3 in scope; nil when the link is at root
	Description *string
	ClickCount  int64
	CreatedAt   *time.Time // optional; set by JSON import
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
		// stack of tag names from H3 headers; pushed at H3, popped at </DL>.
		// Empty H3s push "" so stack depth stays in sync with nesting (a bare
		// </DL> must not pop a parent folder that was never closed).
		tagStack []string
		// pendingTag captures the latest H3 text until we hit a closing tag
		captureTag bool
		pendingTag string
	)
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			err := z.Err()
			if errors.Is(err, io.EOF) {
				return items, nil
			}
			return items, err
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
				// Netscape exports are user-supplied and can carry javascript:,
				// data:, file:, vbscript:, etc. The JSON importer (json.go) and
				// API DTO both reject non-http(s); the Netscape path used to
				// trust href blindly, letting hostile URLs reach link.url and
				// then render as <a href={url}> in LinkDialog or feed the
				// screenshot endpoint (read-anywhere via file://).
				if !isHTTPScheme(href) {
					continue
				}
				title := readText(z)
				it := Item{URL: href, Title: strings.TrimSpace(title)}
				if it.Title == "" {
					it.Title = href
				}
				if len(tagStack) > 0 {
					// Deepest non-empty H3 = folder. Outer non-empty H3s = tags.
					// Empty placeholders (blank H3) keep stack depth but are ignored.
					folderIdx := -1
					for i := len(tagStack) - 1; i >= 0; i-- {
						if tagStack[i] != "" {
							folderIdx = i
							break
						}
					}
					if folderIdx >= 0 {
						f := tagStack[folderIdx]
						it.Folder = &f
						for _, name := range tagStack[:folderIdx] {
							if name != "" {
								it.Tags = append(it.Tags, name)
							}
						}
					}
				}
				items = append(items, it)
				if len(items) > maxImportItems {
					return nil, fmt.Errorf("%w: max %d", ErrTooManyItems, maxImportItems)
				}
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
					// Always push (empty string for blank H3) so </DL> pops
					// match 1:1 with H3 opens — empty names never become folders.
					tagStack = append(tagStack, strings.TrimSpace(pendingTag))
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

// isHTTPScheme reports whether href parses to an http or https URL. Treats
// parse errors, missing schemes, and anything else (javascript:, data:, file:,
// vbscript:, mailto:, tel:, etc.) as rejected.
func isHTTPScheme(href string) bool {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return false
	}
	s := strings.ToLower(u.Scheme)
	return s == "http" || s == "https"
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

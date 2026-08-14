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

type netscapeState uint8

const (
	stateScanning netscapeState = iota
	stateFolderName
	stateLinkTitle
)

type netscapeParser struct {
	tokenizer   *html.Tokenizer
	state       netscapeState
	resumeState netscapeState
	items       []Item
	pendingLink Item
	folderStack []string
	folderName  strings.Builder
}

// ParseNetscape walks a Netscape Bookmark HTML file and returns one Item per
// <A> link. Each <H3> defines a folder scope. The innermost (deepest) H3
// becomes the link's folder; the outer H3s above it become tags applied to
// the link (so a Chrome export's "Bookmarks Bar / Work / Issues" maps to
// folder=Issues with tags=[Bookmarks Bar, Work]). Foldex folders are flat
// (1-level), so nesting collapses to "deepest wins".
func ParseNetscape(r io.Reader) ([]Item, error) {
	p := netscapeParser{tokenizer: html.NewTokenizer(r)}
	return p.parse()
}

func (p *netscapeParser) parse() ([]Item, error) {
	for {
		tokenType := p.tokenizer.Next()
		if tokenType == html.ErrorToken {
			return p.handleTokenizerError(p.tokenizer.Err())
		}
		if err := p.handleToken(tokenType); err != nil {
			return nil, err
		}
	}
}

func (p *netscapeParser) handleTokenizerError(err error) ([]Item, error) {
	if p.state == stateLinkTitle {
		if appendErr := p.finishLink(""); appendErr != nil {
			return nil, appendErr
		}
	}
	return p.result(err)
}

func (p *netscapeParser) result(err error) ([]Item, error) {
	if errors.Is(err, io.EOF) {
		return p.items, nil
	}
	return p.items, err
}

func (p *netscapeParser) handleToken(tokenType html.TokenType) error {
	if p.state == stateLinkTitle {
		return p.handleLinkTitle(tokenType)
	}
	switch tokenType {
	case html.StartTagToken, html.SelfClosingTagToken:
		p.handleStartToken(p.tokenizer.Token())
	case html.TextToken:
		p.handleTextToken(p.tokenizer.Token())
	case html.EndTagToken:
		p.handleEndToken(p.tokenizer.Token())
	}
	return nil
}

func (p *netscapeParser) handleStartToken(token html.Token) {
	switch strings.ToLower(token.Data) {
	case "h3":
		p.state = stateFolderName
		p.folderName.Reset()
	case "a":
		p.beginLink(token)
	}
}

func (p *netscapeParser) handleTextToken(token html.Token) {
	if p.state == stateFolderName {
		p.folderName.WriteString(token.Data)
	}
}

func (p *netscapeParser) handleEndToken(token html.Token) {
	switch strings.ToLower(token.Data) {
	case "h3":
		p.closeFolderName()
	case "dl":
		p.popFolder()
	}
}

func (p *netscapeParser) closeFolderName() {
	if p.state != stateFolderName {
		return
	}
	// Blank placeholders keep malformed and browser-generated DL closes aligned.
	p.folderStack = append(p.folderStack, strings.TrimSpace(p.folderName.String()))
	p.state = stateScanning
	p.folderName.Reset()
}

func (p *netscapeParser) popFolder() {
	if len(p.folderStack) == 0 {
		return
	}
	p.folderStack = p.folderStack[:len(p.folderStack)-1]
}

func (p *netscapeParser) beginLink(token html.Token) {
	href := attr(token, "href")
	if href == "" || !isHTTPScheme(href) {
		return
	}
	p.pendingLink = Item{URL: href}
	p.resumeState = p.state
	p.state = stateLinkTitle
}

func (p *netscapeParser) handleLinkTitle(tokenType html.TokenType) error {
	if tokenType == html.TextToken {
		return p.finishLink(p.tokenizer.Token().Data)
	}
	return p.finishLink("")
}

func (p *netscapeParser) finishLink(title string) error {
	item := p.pendingLink
	item.Title = strings.TrimSpace(title)
	if item.Title == "" {
		item.Title = item.URL
	}
	p.pendingLink = Item{}
	p.state = p.resumeState
	p.applyFolderScope(&item)
	p.items = append(p.items, item)
	if len(p.items) > maxImportItems {
		return fmt.Errorf("%w: max %d", ErrTooManyItems, maxImportItems)
	}
	return nil
}

func (p *netscapeParser) applyFolderScope(item *Item) {
	folderIndex := deepestNonBlank(p.folderStack)
	if folderIndex < 0 {
		return
	}

	folder := p.folderStack[folderIndex]
	item.Folder = &folder
	for _, name := range p.folderStack[:folderIndex] {
		if name != "" {
			item.Tags = append(item.Tags, name)
		}
	}
}

func deepestNonBlank(names []string) int {
	for i := len(names) - 1; i >= 0; i-- {
		if names[i] != "" {
			return i
		}
	}
	return -1
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

package mailer

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"path"
	"slices"
	"strings"
	texttemplate "text/template"
)

// The glob form, and no file here may start with `_`. The directory form
// (`//go:embed templates`) silently EXCLUDES names beginning with `_` or `.`,
// so a chrome file called `_chrome.html.tmpl` would vanish the day someone
// simplified this line — taking every message with it, since they all compose
// its blocks. Naming it `chrome` costs nothing and removes the trap.
//
//go:embed templates/*.tmpl templates/*.json
var templatesFS embed.FS

// DefaultLocale is the source of truth for copy. A locale that is missing, or
// missing one message, falls back to it PER MESSAGE rather than per catalogue:
// a half-translated locale should degrade to English, not to an empty e-mail.
const DefaultLocale = "en"

// Template names. They are stored verbatim in mail_outbox.template, so renaming
// one strands every queued row that carries the old name — add a new name and
// keep the old one resolvable instead.
const (
	TemplateInvite           = "invite"
	TemplatePasswordReset    = "password_reset"
	TemplateResetUnavailable = "reset_unavailable"
	TemplateLoginCode        = "login_code"
	TemplateEnrollEmail2FA   = "enroll_email_2fa"
	TemplateStepUpCode       = "step_up_code"
	TemplateVerifyEmail      = "verify_email"
	TemplateAdminRecovery    = "admin_recovery"
	TemplateSessionRevoked   = "session_revoked"
	TemplateRecoveryCodeUsed = "recovery_code_used"
	TemplateAccountConverted = "account_converted"
)

// ErrUnknownTemplate is returned for a template name no catalogue defines.
//
// It is an error rather than a blank message on purpose: a queued row naming a
// template that was deleted must fail loudly and land in the outbox's failed
// state, not deliver an empty envelope to a user waiting for a reset link.
var ErrUnknownTemplate = errors.New("mailer: unknown template")

// Envelope is a message that has not been rendered yet: which template, to
// whom, and the values that fill it.
//
// This is what crosses the outbox. Storing (template, params) rather than a
// rendered body keeps the stored row small, lets a copy fix apply to messages
// already queued, and is what makes the locale column meaningful — a frozen
// body has already chosen its language.
//
// Params are strings, including counts. The map round-trips through JSON and
// then through encryption, and a map[string]any would decode numbers back as
// float64, so "expires in 30 minutes" would arrive as "expires in 30 minutes"
// only by luck of formatting.
type Envelope struct {
	Template string            `json:"template"`
	To       string            `json:"to"`
	Params   map[string]string `json:"params,omitempty"`
}

// Well-known param keys. ParamActionURL and ParamCode are structural: the
// layout renders a button and a code block when they are present, so a message
// opts into either by supplying the value.
const (
	ParamActionURL      = "ActionURL"
	ParamCode           = "Code"
	ParamExpiresMinutes = "ExpiresMinutes"
	ParamExpiresHours   = "ExpiresHours"
	ParamBy             = "By"
	ParamRemaining      = "Remaining"
	ParamGoogleEmail    = "GoogleEmail"
)

// messageStrings is the localized copy of one message as it appears on disk.
// Every field is itself a template executed against the envelope params, which
// is how a count or an address reaches the middle of a sentence in any
// language's word order.
type messageStrings struct {
	Subject string `json:"subject"`
	// Eyebrow is the small tinted label naming the KIND of message. It is copy,
	// so it lives here: a label hardcoded in a template would ship one
	// language's word to every reader.
	Eyebrow  string   `json:"eyebrow"`
	Heading  string   `json:"heading"`
	Body     []string `json:"body"`
	Action   string   `json:"action"`
	Footnote string   `json:"footnote"`
}

// copyText is one piece of copy, compiled once when the catalogue loads.
//
// Compiling at load rather than per render is not only cheaper — it moves a
// malformed placeholder from "the password-reset e-mail fails to send" to "the
// binary refuses to start", which is the difference between a defect an
// operator sees immediately and one they hear about from a locked-out user.
//
// A string with no placeholder keeps no template at all: most copy is literal,
// and a template engine invoked to return its own input is pure overhead.
type copyText struct {
	literal string
	tmpl    *texttemplate.Template
}

// `missingkey=error` is deliberate. A template referencing a param the caller
// forgot would otherwise render the literal `<no value>` into a password-reset
// e-mail, which is the kind of defect that reaches production because it looks
// like copy rather than a crash.
func compileCopy(text string) (copyText, error) {
	if text == "" || !strings.Contains(text, "{{") {
		return copyText{literal: text}, nil
	}
	t, err := texttemplate.New("copy").Option("missingkey=error").Parse(text)
	if err != nil {
		return copyText{}, fmt.Errorf("parse copy %q: %w", text, err)
	}
	return copyText{literal: text, tmpl: t}, nil
}

// render executes the copy against the params.
//
// text/template rather than html/template even for the HTML arm: the result is
// inserted into the layout as a plain value, so html/template escapes it there,
// in the context it actually lands in. Escaping twice would render `&amp;` to
// the reader.
func (c copyText) render(params map[string]string) (string, error) {
	if c.tmpl == nil {
		return c.literal, nil
	}
	var b strings.Builder
	if err := c.tmpl.Execute(&b, params); err != nil {
		return "", fmt.Errorf("mailer: execute copy: %w", err)
	}
	return b.String(), nil
}

// compiledMessage is messageStrings after compilation — what the renderer
// actually consumes.
type compiledMessage struct {
	subject  copyText
	eyebrow  copyText
	heading  copyText
	body     []copyText
	action   copyText
	footnote copyText
}

func compileMessage(ms messageStrings) (compiledMessage, error) {
	var out compiledMessage
	var err error
	if out.subject, err = compileCopy(ms.Subject); err != nil {
		return out, err
	}
	if out.eyebrow, err = compileCopy(ms.Eyebrow); err != nil {
		return out, err
	}
	if out.heading, err = compileCopy(ms.Heading); err != nil {
		return out, err
	}
	if out.action, err = compileCopy(ms.Action); err != nil {
		return out, err
	}
	if out.footnote, err = compileCopy(ms.Footnote); err != nil {
		return out, err
	}
	for _, p := range ms.Body {
		c, cerr := compileCopy(p)
		if cerr != nil {
			return out, cerr
		}
		out.body = append(out.body, c)
	}
	return out, nil
}

// mailDoc is the small document every message renders into. Both layout arms
// consume it, which is what keeps the text arm from drifting out of parity with
// the HTML one.
type mailDoc struct {
	Locale    string
	Eyebrow   string
	Heading   string
	Body      []string
	Code      string
	Action    string
	ActionURL string
	Footnote  string
	// Footer is the shared sign-off, resolved from the reserved catalogue key
	// footerKey. It is not per message — duplicating it across eleven entries
	// in three locales would be thirty-three places to fix one typo.
	Footer string
}

// footerKey is a reserved catalogue entry rather than a template name.
const footerKey = "_footer"

// isReservedKey answers "is this catalogue entry copy rather than a message?".
//
// It exists because the same question was being answered two different ways —
// the parity check compared against footerKey exactly while TemplateNames
// matched the `_` prefix — and the two only agree while `_footer` is the sole
// reserved key. Adding a second one (`_header`, `_signature`: exactly the kind
// of shared copy the footer precedent invites) made the parity check demand
// `_header.html.tmpl`, and since the assets load into a package-level var that
// is a panic at init. A copy addition must not refuse the binary.
func isReservedKey(name string) bool { return strings.HasPrefix(name, "_") }

type assets struct {
	catalogs map[string]map[string]compiledMessage
	html     *htmltemplate.Template
	text     *texttemplate.Template
}

var std = mustLoadAssets(templatesFS)

func mustLoadAssets(fsys fs.FS) *assets {
	a, err := loadAssets(fsys)
	if err != nil {
		// The templates are embedded at build time, so this cannot be a runtime
		// condition: reaching it means the binary shipped broken assets.
		panic("mailer: " + err.Error())
	}
	return a
}

func loadAssets(fsys fs.FS) (*assets, error) {
	// Every *.html.tmpl at once: _chrome defines the shared blocks and each
	// message defines itself, so the set resolves as one namespace and
	// ExecuteTemplate can select by template name.
	html, err := htmltemplate.New("mail").Funcs(htmltemplate.FuncMap{"dict": dict}).
		ParseFS(fsys, "templates/*.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse html layouts: %w", err)
	}
	text, err := texttemplate.ParseFS(fsys, "templates/*.txt.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse text layout: %w", err)
	}
	names, err := fs.Glob(fsys, "templates/strings.*.json")
	if err != nil {
		return nil, fmt.Errorf("glob catalogues: %w", err)
	}
	catalogs := make(map[string]map[string]compiledMessage, len(names))
	for _, name := range names {
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var cat map[string]messageStrings
		if err := json.Unmarshal(raw, &cat); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		compiled := make(map[string]compiledMessage, len(cat))
		for template, ms := range cat {
			c, cerr := compileMessage(ms)
			if cerr != nil {
				return nil, fmt.Errorf("%s/%s: %w", name, template, cerr)
			}
			compiled[template] = c
		}
		locale := strings.TrimSuffix(strings.TrimPrefix(name, "templates/strings."), ".json")
		catalogs[locale] = compiled
	}
	if _, ok := catalogs[DefaultLocale]; !ok {
		return nil, fmt.Errorf("catalogue for the default locale %q is missing", DefaultLocale)
	}
	// A layout must not redefine a shared block, and that is checked here
	// because nothing else can catch it.
	//
	// text/template's Parse silently REPLACES an existing definition, and
	// ParseFS walks the glob in sorted order — so a `{{define "chrome.button"}}`
	// pasted into any file sorting after chrome.html.tmpl rewrites that block
	// for all eleven messages. Observed blast radius when tried: the password
	// reset rendered with no anchor at all, and the whole suite stayed green.
	// The credential exists in that mail and nowhere else, so the reader is
	// locked out until a cooldown expires. Copy-pasting a sibling is the single
	// most likely way a twelfth message gets added, which is exactly when this
	// fires.
	//
	// The parity check below cannot see it: Lookup still resolves the name.
	if err := refuseDuplicateDefinitions(fsys); err != nil {
		return nil, err
	}
	// Copy and layout must agree, and this is checked at LOAD rather than at
	// send. The assets are embedded, so a mismatch is never a runtime
	// condition — it is a binary that shipped wrong, and the failure belongs
	// where a developer sees it instead of in a queued password reset that
	// fails to render at three in the morning.
	for name := range catalogs[DefaultLocale] {
		if isReservedKey(name) {
			continue
		}
		if html.Lookup(name) == nil {
			return nil, fmt.Errorf("message %q has copy but no HTML layout "+
				"(add templates/%s.html.tmpl)", name, name)
		}
		// BOTH arms, for the same reason the check exists at all: a message
		// with no text layout used to render an empty text part, and `render`
		// only emits multipart/alternative when both arms exist — so the
		// message would silently go out HTML-only, to an audience that
		// self-hosts and disproportionately refuses HTML.
		if text.Lookup("text."+name) == nil {
			return nil, fmt.Errorf("message %q has copy but no text layout "+
				"(add a text.%s definition to templates/layout.txt.tmpl)", name, name)
		}
	}
	return &assets{catalogs: catalogs, html: html, text: text}, nil
}

// refuseDuplicateDefinitions fails when two files define the same template name.
//
// Each file is parsed ALONE into a throwaway template so its definitions can be
// attributed to it; parsing them together is what hides the collision in the
// first place.
func refuseDuplicateDefinitions(fsys fs.FS) error {
	files, err := fs.Glob(fsys, "templates/*.html.tmpl")
	if err != nil {
		return fmt.Errorf("glob layouts: %w", err)
	}
	slices.Sort(files)
	owner := make(map[string]string, len(files)*2)
	for _, f := range files {
		base := path.Base(f)
		t, perr := htmltemplate.New(base).Funcs(htmltemplate.FuncMap{"dict": dict}).ParseFS(fsys, f)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", f, perr)
		}
		for _, sub := range t.Templates() {
			name := sub.Name()
			// The implicit template named after the file is not a definition.
			if name == base {
				continue
			}
			if prev, dup := owner[name]; dup {
				return fmt.Errorf("template %q is defined in both %s and %s; "+
					"a redefinition silently replaces the original for every message", name, prev, f)
			}
			owner[name] = f
		}
	}
	return nil
}

// dict builds a map inside a template, which is how the chrome blocks receive
// more than one value — Go templates pass a single argument, and the button
// needs a URL, a label and the accent colour of the message calling it.
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("mailer: dict needs an even number of arguments")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("mailer: dict key %v is not a string", kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

// Render turns an Envelope into a Message in the recipient's locale.
func Render(env Envelope, locale string) (Message, error) {
	return std.render(env, locale)
}

// TemplateNames lists every message the default catalogue defines, skipping the
// reserved non-message keys. It exists so a test can walk the real set rather
// than a hand-maintained list that drifts the first time a message is added.
func TemplateNames() []string {
	out := make([]string, 0, len(std.catalogs[DefaultLocale]))
	for name := range std.catalogs[DefaultLocale] {
		if isReservedKey(name) {
			continue
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// SupportedLocales lists the catalogues that shipped, sorted for stable output.
func SupportedLocales() []string {
	out := make([]string, 0, len(std.catalogs))
	for l := range std.catalogs {
		out = append(out, l)
	}
	slices.Sort(out)
	return out
}

// NormalizeLocale maps a browser-shaped tag onto a shipped catalogue: `pt-BR`
// and `PT` both resolve to `pt`, anything unknown to the default. Region
// subtags are dropped rather than matched, because the catalogues are per
// language and a `pt-PT` that fell through to English would be worse than a
// `pt-BR` rendering.
func NormalizeLocale(locale string) string {
	l, _ := LookupLocale(locale)
	return l
}

// LookupLocale is NormalizeLocale plus the answer to "was that recognised?".
//
// The distinction only matters where a tag is STORED rather than rendered.
// NormalizeLocale collapses both `pt-BR` and `klingon` to a shipped catalogue,
// which is right for picking a language to render in — never fail a message
// over a locale — but useless for validating a profile field, where the two
// cases must be told apart. A validator built on the normalized value alone can
// only compare it against its input, which then rejects every tag a browser
// actually sends (`pt-BR`, `PT`, `en-US`) as if it were unsupported.
func LookupLocale(locale string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if _, ok := std.catalogs[l]; ok {
		return l, true
	}
	return DefaultLocale, false
}

func (a *assets) render(env Envelope, locale string) (Message, error) {
	ms, err := a.strings(env.Template, locale)
	if err != nil {
		return Message{}, err
	}
	doc := mailDoc{
		Locale:    NormalizeLocale(locale),
		Code:      env.Params[ParamCode],
		ActionURL: env.Params[ParamActionURL],
	}
	// The footer resolves through the same per-message locale fallback, and an
	// absent entry simply renders no footer — a locale that has not been given
	// the sign-off yet degrades to a message without one, never to a broken
	// render.
	if f, ferr := a.strings(footerKey, locale); ferr == nil && len(f.body) > 0 {
		if doc.Footer, err = f.body[0].render(env.Params); err != nil {
			return Message{}, err
		}
	}
	if doc.Eyebrow, err = ms.eyebrow.render(env.Params); err != nil {
		return Message{}, err
	}
	subject, err := ms.subject.render(env.Params)
	if err != nil {
		return Message{}, err
	}
	if doc.Heading, err = ms.heading.render(env.Params); err != nil {
		return Message{}, err
	}
	if doc.Action, err = ms.action.render(env.Params); err != nil {
		return Message{}, err
	}
	if doc.Footnote, err = ms.footnote.render(env.Params); err != nil {
		return Message{}, err
	}
	for _, p := range ms.body {
		para, perr := p.render(env.Params)
		if perr != nil {
			return Message{}, perr
		}
		if para != "" {
			doc.Body = append(doc.Body, para)
		}
	}

	var htmlOut, textOut strings.Builder
	// ExecuteTemplate, not Execute: each message owns its layout now.
	if err := a.html.ExecuteTemplate(&htmlOut, env.Template, doc); err != nil {
		return Message{}, fmt.Errorf("mailer: render html %q: %w", env.Template, err)
	}
	if err := a.text.ExecuteTemplate(&textOut, "text."+env.Template, doc); err != nil {
		return Message{}, fmt.Errorf("mailer: render text %q: %w", env.Template, err)
	}
	return Message{
		To:      env.To,
		Subject: subject,
		Text:    strings.TrimSpace(textOut.String()) + "\n",
		HTML:    htmlOut.String(),
	}, nil
}

// strings resolves the copy for one message, falling back to DefaultLocale when
// the requested locale does not carry it.
func (a *assets) strings(template, locale string) (compiledMessage, error) {
	if ms, ok := a.catalogs[NormalizeLocale(locale)][template]; ok {
		return ms, nil
	}
	if ms, ok := a.catalogs[DefaultLocale][template]; ok {
		return ms, nil
	}
	return compiledMessage{}, fmt.Errorf("%w: %q", ErrUnknownTemplate, template)
}

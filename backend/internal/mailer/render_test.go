package mailer

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every message a constructor can produce, so the parity checks below cannot
// quietly stop covering one.
func allEnvelopes() []Envelope {
	return []Envelope{
		InviteMessage("a@b.c", "Ana", "https://foldex.test/#invite=T", 168),
		InviteMessage("a@b.c", "", "https://foldex.test/#invite=T", 168),
		PasswordResetMessage("a@b.c", "https://foldex.test/#reset=T", 30),
		PasswordResetUnavailableMessage("a@b.c"),
		LoginCodeMessage("a@b.c", "123456", 5),
		EnrollEmail2FAMessage("a@b.c", "123456", 5),
		StepUpCodeMessage("a@b.c", "123456", 5),
		VerifyEmailMessage("a@b.c", "https://foldex.test/#verify=T", 30),
		AdminPasswordRecoveryMessage("a@b.c", "https://foldex.test/#reset=T", 30),
		SessionRevokedMessage("a@b.c"),
		RecoveryCodeUsedMessage("a@b.c", 7),
		AccountConvertedMessage("a@b.c", "person@gmail.com"),
	}
}

// Locale parity is the whole reason the copy lives in JSON. A catalogue that
// silently lost a message would fall back to English, which reads as a
// translation bug nobody reports.
func TestEveryMessageRendersInEveryLocale(t *testing.T) {
	t.Parallel()
	locales := SupportedLocales()
	require.ElementsMatch(t, []string{"en", "es", "pt"}, locales)

	for _, env := range allEnvelopes() {
		for _, locale := range locales {
			ms, ok := std.catalogs[locale][env.Template]
			require.True(t, ok, "locale %q has no copy for %q", locale, env.Template)
			assert.NotEmpty(t, ms.subject.literal, "%s/%s subject", locale, env.Template)
			assert.NotEmpty(t, ms.heading.literal, "%s/%s heading", locale, env.Template)

			m, err := Render(env, locale)
			require.NoError(t, err, "%s/%s", locale, env.Template)
			assert.NotEmpty(t, m.Subject)
			// The text arm is mandatory on EVERY message: render only emits
			// multipart/alternative when both arms exist, so a message with an
			// empty text part is unreadable in a client that refuses HTML.
			assert.NotEmpty(t, strings.TrimSpace(m.Text), "%s/%s text arm", locale, env.Template)
			assert.NotEmpty(t, m.HTML, "%s/%s html arm", locale, env.Template)
			assert.NotContains(t, m.Text, "<no value>",
				"%s/%s references a param the constructor does not set", locale, env.Template)
			assert.NotContains(t, m.Subject, "<no value>")
		}
	}
}

// A message whose copy references a param must have that param set by its
// constructor, in EVERY locale. `missingkey=error` is what turns the mistake
// into a failure instead of `<no value>` printed into a reset e-mail.
func TestMissingParamIsAnErrorRatherThanPrintedCopy(t *testing.T) {
	t.Parallel()
	// The invite heading is conditional on .By, which makes it the one piece of
	// copy that would silently accept an absent key.
	_, err := Render(Envelope{Template: TemplateInvite, To: "a@b.c"}, "en")
	assert.Error(t, err, "an envelope with no params must fail rather than render half a sentence")
}

func TestUnknownTemplateIsAnError(t *testing.T) {
	t.Parallel()
	_, err := Render(Envelope{Template: "no_such_message", To: "a@b.c"}, "en")
	assert.ErrorIs(t, err, ErrUnknownTemplate)
}

func TestNormalizeLocale(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"pt": "pt", "pt-BR": "pt", "PT_pt": "pt", "es-419": "es",
		"en": "en", "": "en", "de": "en", "zz-ZZ": "en",
	}
	for in, want := range cases {
		assert.Equal(t, want, NormalizeLocale(in), "input %q", in)
	}
}

// A half-translated catalogue must degrade PER MESSAGE, not per catalogue: the
// alternative is an empty e-mail where a reset link should be.
func TestAMissingMessageFallsBackToEnglish(t *testing.T) {
	t.Parallel()
	ms, err := std.strings(TemplatePasswordReset, "pt")
	require.NoError(t, err)
	assert.NotEqual(t, std.catalogs["en"][TemplatePasswordReset].subject.literal, ms.subject.literal,
		"the pt catalogue is not actually translated")

	// A locale that carries nothing at all resolves through the same path.
	ms, err = std.strings(TemplatePasswordReset, "de")
	require.NoError(t, err)
	assert.Equal(t, std.catalogs["en"][TemplatePasswordReset].subject.literal, ms.subject.literal)
}

// The HTML arm escapes in context. An inviter name is admin-supplied and
// reaches the middle of a heading.
func TestParamsCannotBreakOutOfTheirElement(t *testing.T) {
	t.Parallel()
	m, err := Render(InviteMessage("a@b.c", `Ana"><script>alert(1)</script>`,
		"https://foldex.test/#invite=T", 1), "en")
	require.NoError(t, err)
	assert.NotContains(t, m.HTML, "<script>")
	assert.Contains(t, m.HTML, "&lt;script&gt;")
}

// html/template's URL context is what neutralises a scheme that should never
// reach an href. Nothing constructs one today; this locks the property so a
// future template cannot lose it by moving the value.
func TestAJavascriptActionURLIsNeutralised(t *testing.T) {
	t.Parallel()
	m, err := Render(Envelope{
		Template: TemplatePasswordReset, To: "a@b.c",
		Params: map[string]string{
			ParamActionURL:      "javascript:alert(1)",
			ParamExpiresMinutes: "30",
		},
	}, "en")
	require.NoError(t, err)
	assert.NotContains(t, m.HTML, `href="javascript:`)
	// Pin the anchor and the neutralisation MARKER. Without these the assertion
	// above is satisfied by a layout that stopped rendering a button at all —
	// which is precisely the change this test exists to catch, since moving the
	// value out of a URL context and deleting it produce the same observable.
	assert.Contains(t, m.HTML, "<a ", "a deleted button must not read as a safe one")
	assert.Contains(t, m.HTML, "#ZgotmplZ", "html/template's URL filter must have fired")
}

func TestLoginCodeKeepsTheCodeInTheSubject(t *testing.T) {
	t.Parallel()
	for _, locale := range SupportedLocales() {
		m, err := Render(LoginCodeMessage("a@b.c", "914022", 5), locale)
		require.NoError(t, err)
		// Reading the code from a phone's notification preview is how most
		// people actually use this message.
		assert.Contains(t, m.Subject, "914022", "locale %s", locale)
		assert.Contains(t, m.Text, "914022", "locale %s", locale)
	}
}

func TestLoadAssetsRefusesBrokenInput(t *testing.T) {
	t.Parallel()
	good := map[string]*fstest.MapFile{
		// Each message now owns a layout, so the smallest valid asset set is a
		// catalogue entry plus the template that renders it.
		"templates/x.html.tmpl":     {Data: []byte(`{{ define "x" }}<p>{{ .Heading }}</p>{{ end }}`)},
		"templates/layout.txt.tmpl": {Data: []byte(`{{ define "text.x" }}{{ .Heading }}{{ end }}`)},
		"templates/strings.en.json": {Data: []byte(`{"x":{"subject":"s","heading":"h"}}`)},
	}
	_, err := loadAssets(fstest.MapFS(good))
	require.NoError(t, err)

	cases := map[string]func(map[string]*fstest.MapFile){
		"unparsable html layout": func(m map[string]*fstest.MapFile) {
			m["templates/x.html.tmpl"] = &fstest.MapFile{Data: []byte(`{{ define "x" }}{{ .Heading`)}
		},
		// The parity check. Copy without a layout used to render an empty
		// document at SEND time; it now refuses the binary, because the assets
		// are embedded and a mismatch is never a runtime condition.
		"copy with no layout": func(m map[string]*fstest.MapFile) {
			m["templates/strings.en.json"] = &fstest.MapFile{
				Data: []byte(`{"x":{"subject":"s","heading":"h"},"y":{"subject":"s","heading":"h"}}`)}
		},
		// The reserved footer key is copy that intentionally has no layout of
		// its own; the check must skip it rather than demand _footer.html.tmpl.
		"reserved footer key is not a message": func(m map[string]*fstest.MapFile) {
			// Deliberately NOT an error case — asserted separately below.
			m["templates/strings.en.json"] = &fstest.MapFile{
				Data: []byte(`{"x":{"subject":"s","heading":"h"},"_footer":{"body":["f"]}}`)}
		},
		"unparsable text layout": func(m map[string]*fstest.MapFile) {
			m["templates/layout.txt.tmpl"] = &fstest.MapFile{Data: []byte(`{{ define "text.x" }}{{ if }}`)}
		},
		// The text arm is mandatory: render only emits multipart/alternative
		// when both exist, so a message missing it silently ships HTML-only to
		// the audience that most often refuses HTML.
		"copy with no text layout": func(m map[string]*fstest.MapFile) {
			m["templates/layout.txt.tmpl"] = &fstest.MapFile{Data: []byte(`{{ define "text.other" }}x{{ end }}`)}
		},
		"malformed catalogue": func(m map[string]*fstest.MapFile) {
			m["templates/strings.en.json"] = &fstest.MapFile{Data: []byte(`{`)}
		},
		"no default locale": func(m map[string]*fstest.MapFile) {
			delete(m, "templates/strings.en.json")
			m["templates/strings.pt.json"] = &fstest.MapFile{Data: []byte(`{}`)}
		},
		// The reason copy is compiled at LOAD and not at render: a malformed
		// placeholder must fail the boot, not the password-reset e-mail of
		// whoever happens to ask for one first.
		"unparsable copy placeholder": func(m map[string]*fstest.MapFile) {
			m["templates/strings.en.json"] = &fstest.MapFile{
				Data: []byte(`{"x":{"subject":"s","heading":"hi {{ .Name"}}`)}
		},
	}
	for name, mutate := range cases {
		if name == "reserved footer key is not a message" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			broken := map[string]*fstest.MapFile{}
			for k, v := range good {
				broken[k] = v
			}
			mutate(broken)
			_, err := loadAssets(fstest.MapFS(broken))
			assert.Error(t, err)
		})
	}

	t.Run("reserved footer key is not a message", func(t *testing.T) {
		withFooter := map[string]*fstest.MapFile{}
		for k, v := range good {
			withFooter[k] = v
		}
		cases["reserved footer key is not a message"](withFooter)
		_, err := loadAssets(fstest.MapFS(withFooter))
		require.NoError(t, err, "_footer is copy, not a message, and needs no layout")
	})
}

// The two messages that must carry no link now carry none STRUCTURALLY: the
// notice layout has no button and no URL box at all.
//
// Before the layouts were split, "no link" held only because the copy never
// supplied an ActionURL — a property one careless param away from breaking.
// session_revoked is anti-phishing (it reports killed sessions, the exact
// pretext a forgery uses), and reset_unavailable is ADR-31 (a link there would
// let mailbox control alone resurrect a password credential). Both are asserted
// with the param DELIBERATELY injected, which is the case that used to work.
func TestLinklessMessagesCannotBeGivenALink(t *testing.T) {
	t.Parallel()
	for _, template := range []string{TemplateSessionRevoked, TemplateResetUnavailable} {
		for _, locale := range SupportedLocales() {
			m, err := Render(Envelope{
				Template: template, To: "a@b.c",
				Params: map[string]string{
					ParamActionURL: "https://attacker.test/harvest",
					ParamCode:      "000000",
				},
			}, locale)
			require.NoError(t, err, "%s/%s", template, locale)
			assert.NotContains(t, m.HTML, "<a ", "%s/%s must render no anchor", template, locale)
			assert.NotContains(t, m.HTML, "attacker.test", "%s/%s", template, locale)
			// BOTH arms. Asserting only the HTML certified half a guarantee:
			// the shared text layout keyed its link block on .ActionURL, so it
			// printed the injected URL — under a stray empty-label colon — two
			// lines above the footnote promising the message carries no link.
			// Every text client auto-linkifies a bare https://, and a
			// multipart/alternative reader who refuses HTML sees only that arm.
			assert.NotContains(t, m.Text, "attacker.test", "%s/%s text arm", template, locale)
			assert.NotContains(t, m.Text, "000000", "%s/%s text arm", template, locale)
		}
	}
}

// Walks the CATALOGUE, not the hand-maintained allEnvelopes list.
//
// That list is what every other test iterates, and it drifts: a message added
// to the copy and to a layout, but whose constructor nobody added to the list,
// would be covered by nothing. TemplateNames reads the shipped catalogue, so
// the set cannot fall behind. The parity check at load proves a layout EXISTS;
// this proves it EXECUTES, and that the two new per-locale pieces — the eyebrow
// label and the shared sign-off — actually reach the document.
func TestEveryCatalogueMessageHasALayoutThatExecutes(t *testing.T) {
	t.Parallel()
	params := map[string]string{
		ParamActionURL: "https://foldex.test/#t=abc", ParamCode: "492817",
		ParamExpiresMinutes: "30", ParamExpiresHours: "48",
		ParamBy: "Ana", ParamRemaining: "7", ParamGoogleEmail: "a@gmail.com",
	}
	names := TemplateNames()
	require.NotEmpty(t, names)
	require.NotContains(t, names, footerKey, "the reserved sign-off is copy, not a message")

	for _, template := range names {
		for _, locale := range SupportedLocales() {
			m, err := Render(Envelope{Template: template, To: "a@b.c", Params: params}, locale)
			require.NoErrorf(t, err, "%s/%s", template, locale)

			ms := std.catalogs[locale][template]
			require.NotEmpty(t, ms.eyebrow.literal, "%s/%s has no eyebrow label", locale, template)
			assert.Contains(t, m.HTML, ms.eyebrow.literal, "%s/%s eyebrow never rendered", locale, template)

			footer := std.catalogs[locale][footerKey].body[0].literal
			require.NotEmpty(t, footer, "locale %q lost the shared sign-off", locale)
			assert.Contains(t, m.HTML, footer, "%s/%s sign-off never rendered", locale, template)

			// Everything BETWEEN the eyebrow and the sign-off was unasserted:
			// heading, body and footnote could each be dropped from all eleven
			// layouts with a green suite. The footnote is the sharpest — every
			// expiry window and every "if this was not you" warning lives there.
			heading, err := ms.heading.render(params)
			require.NoError(t, err)
			assert.GreaterOrEqualf(t, strings.Count(m.HTML, heading), 2,
				"%s/%s renders the heading only once — the hidden preheader alone "+
					"satisfies a Contains check, so the visible <h1> may be gone", locale, template)
			for _, para := range ms.body {
				rendered, perr := para.render(params)
				require.NoError(t, perr)
				if rendered != "" {
					assert.Containsf(t, m.HTML, rendered, "%s/%s lost a body paragraph", locale, template)
					assert.Containsf(t, m.Text, rendered, "%s/%s text arm lost a body paragraph", locale, template)
				}
			}
			if note, ferr := ms.footnote.render(params); ferr == nil && note != "" {
				assert.Containsf(t, m.HTML, note, "%s/%s lost its footnote", locale, template)
				assert.Containsf(t, m.Text, note, "%s/%s text arm lost its footnote", locale, template)
			}
		}
	}
}

// Every message that CARRIES a link must render it — in both arms.
//
// The counterpart to the linkless test, and its absence made that one weaker
// than it read: `NotContains(m.HTML, "<a ")` is satisfied just as well by a tree
// where no message renders an anchor at all. Deleting the whole button+urlbox
// block from password_reset and verify_email left the suite green, and the
// reset link is the highest-consequence string this system produces — it exists
// in that message and nowhere else, because the table keeps only a sha256.
//
// Driven off the catalogue: a message carries a link exactly when its copy
// defines an `action` label, so a twelfth message is covered by construction.
func TestEveryLinkCarryingMessageRendersItsLinkInBothArms(t *testing.T) {
	t.Parallel()
	const sentinel = "https://foldex.test/#t=SENTINEL"
	params := map[string]string{
		ParamActionURL: sentinel, ParamCode: "492817",
		ParamExpiresMinutes: "30", ParamExpiresHours: "48",
		ParamBy: "Ana", ParamRemaining: "7", ParamGoogleEmail: "a@gmail.com",
	}
	carriers := 0
	for _, template := range TemplateNames() {
		for _, locale := range SupportedLocales() {
			ms := std.catalogs[locale][template]
			if ms.action.literal == "" {
				continue
			}
			carriers++
			m, err := Render(Envelope{Template: template, To: "a@b.c", Params: params}, locale)
			require.NoErrorf(t, err, "%s/%s", template, locale)
			assert.Containsf(t, m.HTML, `href="`+sentinel+`"`, "%s/%s lost its button", template, locale)
			assert.Containsf(t, m.HTML, sentinel, "%s/%s lost its URL box", template, locale)
			assert.Containsf(t, m.Text, sentinel, "%s/%s text arm lost the link", template, locale)
		}
	}
	require.NotZero(t, carriers, "no message declares an action — the loop proved nothing")
}

// The catalogue is the source of truth for which messages exist, and
// allEnvelopes is the only place the copy is checked against the params a real
// CONSTRUCTOR supplies — `missingkey=error` turns a mismatch into a render
// failure at DELIVERY, i.e. a second-factor code that never arrives.
//
// The list had already drifted: two messages shipped without one.
func TestEveryShippedMessageHasAConstructorEnvelope(t *testing.T) {
	t.Parallel()
	covered := map[string]bool{}
	for _, env := range allEnvelopes() {
		covered[env.Template] = true
	}
	for _, name := range TemplateNames() {
		assert.Truef(t, covered[name],
			"%s ships but no constructor envelope exercises it against its copy", name)
	}
}

// A second reserved key must be copy, not a demand for a layout.
//
// The parity check compared against footerKey exactly while TemplateNames
// matched the `_` prefix; the two agree only while `_footer` is the sole
// reserved key. Adding `_header` made the check demand `_header.html.tmpl`, and
// the assets load into a package-level var — so a copy addition panicked the
// binary at init.
func TestASecondReservedKeyIsNotTreatedAsAMessage(t *testing.T) {
	t.Parallel()
	_, err := loadAssets(fstest.MapFS(map[string]*fstest.MapFile{
		"templates/x.html.tmpl":     {Data: []byte(`{{ define "x" }}<p>{{ .Heading }}</p>{{ end }}`)},
		"templates/layout.txt.tmpl": {Data: []byte(`{{ define "text.x" }}{{ .Heading }}{{ end }}`)},
		"templates/strings.en.json": {Data: []byte(
			`{"x":{"subject":"s","heading":"h"},"_footer":{"body":["f"]},"_header":{"body":["h"]}}`)},
	}))
	require.NoError(t, err, "reserved keys are copy and need no layout")
}

// A layout that redefines a shared block silently replaces it for EVERY
// message, because text/template's Parse overwrites without error and ParseFS
// walks the glob in sorted order. Observed before the guard: a `chrome.button`
// pasted into one file removed the anchor from the password reset — with the
// whole suite green. Copy-pasting a sibling is how a twelfth message gets
// added, which is exactly when this fires.
func TestALayoutCannotSilentlyRedefineASharedBlock(t *testing.T) {
	t.Parallel()
	_, err := loadAssets(fstest.MapFS(map[string]*fstest.MapFile{
		"templates/chrome.html.tmpl": {Data: []byte(`{{ define "chrome.b" }}CHROME{{ end }}`)},
		// Sorts AFTER chrome, so its definition is the one that would win.
		"templates/z.html.tmpl": {Data: []byte(
			`{{ define "z" }}{{ template "chrome.b" }}{{ end }}{{ define "chrome.b" }}HIJACK{{ end }}`)},
		"templates/layout.txt.tmpl": {Data: []byte(`{{ define "text.z" }}{{ .Heading }}{{ end }}`)},
		"templates/strings.en.json": {Data: []byte(`{"z":{"subject":"s","heading":"h"}}`)},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chrome.b")
	assert.Contains(t, err.Error(), "z.html.tmpl")
}

func TestDictRejectsMalformedArguments(t *testing.T) {
	t.Parallel()
	_, err := dict("a")
	require.Error(t, err, "an odd argument count is a template bug, not a nil map")
	assert.Contains(t, err.Error(), "even number")

	_, err = dict(1, "v")
	require.Error(t, err, "a non-string key cannot index a map[string]any")
	assert.Contains(t, err.Error(), "not a string")

	m, err := dict("k", "v")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"k": "v"}, m)
}

// The accent is a stated signal — red for alert, amber for administrative — and
// it is the one thing about session_revoked that a reader takes in before any
// word of it. Swapping it for the routine indigo left the suite green.
func TestAlertMessagesRenderTheAlertAccent(t *testing.T) {
	t.Parallel()
	const alertRed, routineIndigo = "#c0392f", "#6f6bd8"
	for _, locale := range SupportedLocales() {
		m, err := Render(SessionRevokedMessage("a@b.c"), locale)
		require.NoError(t, err)
		assert.Containsf(t, m.HTML, alertRed, "session_revoked must read as an alert (%s)", locale)
		assert.NotContainsf(t, m.HTML, routineIndigo, "and must not read as routine (%s)", locale)
	}
}

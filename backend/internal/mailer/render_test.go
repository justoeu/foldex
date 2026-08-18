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
		"templates/layout.html.tmpl": {Data: []byte(`<p>{{ .Heading }}</p>`)},
		"templates/layout.txt.tmpl":  {Data: []byte(`{{ .Heading }}`)},
		"templates/strings.en.json":  {Data: []byte(`{"x":{"subject":"s","heading":"h"}}`)},
	}
	_, err := loadAssets(fstest.MapFS(good))
	require.NoError(t, err)

	cases := map[string]func(map[string]*fstest.MapFile){
		"unparsable html layout": func(m map[string]*fstest.MapFile) {
			m["templates/layout.html.tmpl"] = &fstest.MapFile{Data: []byte(`{{ .Heading`)}
		},
		"unparsable text layout": func(m map[string]*fstest.MapFile) {
			m["templates/layout.txt.tmpl"] = &fstest.MapFile{Data: []byte(`{{ if }}`)}
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
}

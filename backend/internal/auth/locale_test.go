package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/mailer"
	"foldex/internal/pkg/authctx"
)

// localeFrom decides the language of every credential e-mail the instance
// sends, and it is pure logic with no dependency to hide behind. A regression
// here mails everyone in English and nobody reports it as a bug.
func TestLocaleFrom(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, header, want string
	}{
		{"absent", "", "en"},
		{"exact match", "pt", "pt"},
		{"region subtag is dropped", "pt-BR", "pt"},
		{"case is irrelevant", "ES-419", "es"},
		{"quality values are stripped", "es;q=0.9", "es"},
		{"first supported tag wins", "pt-BR,en;q=0.8", "pt"},
		// The one that motivates the NormalizeLocale round-trip inside the loop:
		// an unsupported first tag must not stop the scan. NormalizeLocale answers
		// "en" for anything it does not carry, so without distinguishing "matched
		// en" from "matched nothing", `de` would end the search and this reader
		// would get English instead of the Portuguese they also asked for.
		{"unsupported tag falls through to the next", "de-DE,pt;q=0.5", "pt"},
		{"all unsupported", "de,fr,ja", "en"},
		{"wildcard is skipped", "*,es", "es"},
		{"empty entries are skipped", ",,pt", "pt"},
		{"english is honoured explicitly", "en-GB,pt;q=0.5", "en"},
		{"whitespace is tolerated", "  pt-BR , en ", "pt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Accept-Language", tc.header)
			}
			assert.Equal(t, tc.want, localeFrom(r))
		})
	}
}

// The notification helpers run with whatever request triggered them, and one of
// them (the session-reuse warning) is reachable from a path where a nil request
// would be an easy mistake to make later.
func TestLocaleFromNilRequest(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "en", localeFrom(nil))
}

// A repository built without the outbox option must REFUSE a draft rather than
// drop it. The silent-nil direction would be a credential minted with its
// e-mail quietly discarded — precisely the failure the outbox exists to remove.
func TestRepositoryWithoutAnOutboxRefusesMail(t *testing.T) {
	t.Parallel()
	r := NewRepository(nil)

	assert.Error(t, r.EnqueueMail(context.Background(),
		mailer.SessionRevokedMessage("a@b.c"), "en"))

	// A draft that builds nothing is a caller opting out, and stays a no-op.
	assert.NoError(t, r.enqueueDraft(context.Background(), nil, MailDraft{}, "tok"))

	assert.Error(t, r.enqueueDraft(context.Background(), nil, MailDraft{
		Build: func(string) mailer.Envelope { return mailer.SessionRevokedMessage("a@b.c") },
	}, "tok"))
}

// The recipient's own preference outranks the header of whoever triggered the
// message. That order is the entire reason app_user.locale exists: an invitation
// sent by an English-speaking administrator used to reach a Portuguese-speaking
// user in English, because the admin's header was the only signal available.
func TestLocaleForPrefersTheRecipientOverTheSender(t *testing.T) {
	t.Parallel()
	senderIsEnglish := httptest.NewRequest(http.MethodGet, "/", nil)
	senderIsEnglish.Header.Set("Accept-Language", "en-GB")

	assert.Equal(t, "pt", localeFor("pt", senderIsEnglish),
		"the recipient's stored preference must win over the sender's header")
	assert.Equal(t, "en", localeFor("", senderIsEnglish),
		"with no preference the sender's header is the best signal available")

	senderIsSpanish := httptest.NewRequest(http.MethodGet, "/", nil)
	senderIsSpanish.Header.Set("Accept-Language", "es")
	assert.Equal(t, "es", localeFor("", senderIsSpanish))

	// A stored value is normalized like any other: a preference saved as pt-BR
	// by an older client still resolves to the catalogue that exists.
	assert.Equal(t, "pt", localeFor("pt-BR", senderIsSpanish))
	// And an unknown stored value degrades instead of breaking the send.
	assert.Equal(t, "en", localeFor("kl", senderIsSpanish))
}

// ptr is a local helper for the tri-state hint below.
func ptr(s string) *string { return &s }

// The hint's whole reason for existing: the interface language and
// Accept-Language are separate browser settings, and a screen speaking
// Portuguese while the header says English mailed an English reset link.
func TestLocaleForHinted_UsesTheDisplayedLanguageWhenTheAccountHasNoPreference(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Accept-Language", "en-US,en;q=0.9")

	assert.Equal(t, "pt", localeForHinted("", ptr("pt"), r))
	// Browser tags, not just catalogue keys — the SPA hands over whatever
	// i18next resolved, and refusing `pt-BR` would refuse the common case.
	assert.Equal(t, "pt", localeForHinted("", ptr("pt-BR"), r))
}

// The hint is ranked BELOW the recipient's own stored preference, and that
// ordering is what makes it safe on an unauthenticated endpoint: naming a
// language must never change the language of someone who chose one.
func TestLocaleForHinted_NeverOutranksTheRecipientsOwnPreference(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Accept-Language", "en-US,en;q=0.9")

	assert.Equal(t, "es", localeForHinted("es", ptr("pt"), r))
}

// Absent or unrecognised falls through to the header exactly as before, rather
// than being stored, echoed, or collapsing the answer to English.
func TestLocaleForHinted_FallsThroughToTheHeader(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Accept-Language", "es-ES,es;q=0.9")

	assert.Equal(t, "es", localeForHinted("", nil, r), "no hint")
	assert.Equal(t, "es", localeForHinted("", ptr("kl-KL"), r), "unrecognised hint")
	// An empty hint is the SPA sending a field it could not fill, not a request
	// for English.
	assert.Equal(t, "es", localeForHinted("", ptr(""), r), "empty hint")
}

// localeFor is the unhinted path every other call site still uses, and it must
// keep behaving identically.
func TestLocaleFor_IsUnchangedByTheHintParameter(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Accept-Language", "pt-BR")

	assert.Equal(t, "pt", localeFor("", r))
	assert.Equal(t, "es", localeFor("es", r))
	assert.Equal(t, mailer.DefaultLocale, localeFor("", httptest.NewRequest(http.MethodPost, "/", nil)))
}

// The owner guard exists in the handler AND here, and each survives the other
// being deleted — so neither had a witness of its own. With both gone the
// single-owner index still refuses, but as a 500 rather than a 400, which is
// the difference between an honest refusal and an opaque failure.
//
// A nil pool is enough: the guard runs before any query, and reaching the
// database would mean the guard did not.
func TestAdminCreateUserRefusesTheOwnerRoleBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()
	r := NewRepository(nil)

	_, err := r.AdminCreateUser(context.Background(),
		"usurper@example.com", "X", "a fine temporary password", authctx.RoleOwner)
	assert.ErrorIs(t, err, ErrInvalidRole)

	_, err = r.AdminCreateUser(context.Background(),
		"bogus@example.com", "X", "a fine temporary password", authctx.Role("superuser"))
	assert.ErrorIs(t, err, ErrInvalidRole)
}

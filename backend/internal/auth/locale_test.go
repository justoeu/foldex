package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"foldex/internal/mailer"
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

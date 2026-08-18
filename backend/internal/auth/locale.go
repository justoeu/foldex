package auth

import (
	"net/http"
	"strings"

	"foldex/internal/mailer"
)

// localeFor picks the language of one message.
//
// The RECIPIENT's own preference wins, then the Accept-Language of whoever
// triggered the send, then English. That order is the whole point of the profile
// field: an invitation sent by an English-speaking administrator used to reach a
// Portuguese-speaking user in English, because the only signal available was the
// admin's header.
//
// An invitation is the one case the order cannot help — the invitee has no
// account yet, so there is no preference to read and the header is genuinely all
// there is. That is inherent to inviting someone, not a gap in the lookup.
func localeFor(preference string, r *http.Request) string {
	if preference != "" {
		return mailer.NormalizeLocale(preference)
	}
	return localeFrom(r)
}

// localeFrom reads the language out of the triggering request.
//
// It is the fallback, and the only source for an invitation, whose recipient
// has no account to hold a preference.
//
// Parsing is intentionally shallow. Accept-Language's q-values order
// alternatives, and honouring them properly means a parser plus a matcher for
// three catalogues; taking the first tag the catalogues recognise gets the same
// answer for every browser that has ever been configured by a human.
func localeFrom(r *http.Request) string {
	if r == nil {
		return mailer.DefaultLocale
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if tag == "" || tag == "*" {
			continue
		}
		// NormalizeLocale answers DefaultLocale for anything it does not carry,
		// so an explicit round-trip is what tells "matched en" apart from
		// "matched nothing" — otherwise a browser asking for de-DE would stop
		// the scan at the first tag instead of trying the next one.
		if l := mailer.NormalizeLocale(tag); l != mailer.DefaultLocale || strings.HasPrefix(strings.ToLower(tag), mailer.DefaultLocale) {
			return l
		}
	}
	return mailer.DefaultLocale
}

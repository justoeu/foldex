package auth

import (
	"net/http"
	"strings"

	"foldex/internal/mailer"
)

// localeFrom picks the language for an e-mail from the request that triggered
// it.
//
// The header, not a stored preference: there is no `app_user.locale` column
// yet, and the request is the only signal available for the flows that matter
// most — forgot-password and invite acceptance both run for someone who is not
// signed in, so there is no account to read a preference from anyway.
//
// The known cost, and it is real: a message triggered by a DIFFERENT actor
// takes that actor's language. An invitation sent by an English-speaking
// administrator reaches a Portuguese-speaking invitee in English. Fixing that
// needs a column on app_user, which is a deliberate follow-up rather than
// something to guess at here.
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

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
	return localeForHinted(preference, nil, r)
}

// localeForHinted is localeFor plus the language the CALLER says it is
// displaying — used by the anonymous flows, where the request that triggers the
// message comes from a screen the user is looking at.
//
// It exists because the browser speaks with two voices that routinely disagree.
// The interface picks its language from `navigator.language` (and from the
// user's own choice in the topbar, stored per device); the header below is
// `Accept-Language`, a separate setting almost nobody configures. A Chrome
// showing a Portuguese foldex while sending `Accept-Language: en` is an
// ordinary configuration, and it mailed an English password-reset link to a
// user whose every screen was Portuguese.
//
// The hint ranks exactly where the header does — BELOW the recipient's stored
// preference, never above it. That ordering is what keeps it safe on an
// unauthenticated endpoint: the worst an attacker can do by naming a language
// is choose the wording of a message they already caused to be sent, to an
// address they do not control, and only for an account that never stated a
// preference. An unrecognised value falls through to the header rather than
// being stored or echoed.
func localeForHinted(preference string, hint *string, r *http.Request) string {
	if preference != "" {
		return mailer.NormalizeLocale(preference)
	}
	if hint != nil {
		if l, ok := mailer.LookupLocale(*hint); ok {
			return l
		}
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

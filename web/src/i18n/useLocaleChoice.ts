import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { SUPPORTED_LOCALES, type LocaleCode } from '.'
import { useAuth, useCurrentUser } from '../auth/AuthProvider'
import * as auth from '../api/auth'

type LocaleChoice = {
  current: (typeof SUPPORTED_LOCALES)[number]
  choose: (code: LocaleCode) => void
}

/**
 * The one place a language pick is applied, shared by the topbar picker and
 * the auth screens' flag row.
 *
 * Two surfaces offer the same setting, and the write-through to
 * `app_user.locale` is the part that must not diverge: without it the account
 * keeps its old preference and `useAccountLocale` re-applies it on the next
 * load, silently undoing a choice the user watched take effect. A second copy
 * of that rule would drift on exactly the surface nobody re-tests.
 *
 * The account write is skipped when there is no session. `useCurrentUser` is
 * non-null only for `authenticated`, and every screen below `AuthGate`'s
 * authenticated short-circuit — login, setup, invite, reset, forgot,
 * two-factor, enrollment, convert — resolves to null, so the pick there reaches
 * i18next and stops.
 *
 * Writes are SERIALIZED, and that is not decoration. The dropdown closed on
 * pick, which made a second pick cost a reopen; the flag row is three buttons
 * that stay clickable, so trying all three inside one round-trip is an ordinary
 * gesture. Fired concurrently, the responses can settle in any order and the
 * account is left holding whichever landed last rather than whichever was
 * clicked last — a value `useAccountLocale` then imposes on the next load,
 * which is precisely the failure the write-through exists to prevent. Holding
 * one request at a time and re-issuing the latest choice afterwards makes the
 * last click win, in order.
 */
export function useLocaleChoice(): LocaleChoice {
  const { i18n } = useTranslation()
  const user = useCurrentUser()
  const { adopt } = useAuth()
  const desired = useRef<LocaleCode | null>(null)
  const writing = useRef(false)

  const current =
    SUPPORTED_LOCALES.find((l) => l.code === (i18n.resolvedLanguage ?? i18n.language)) ??
    SUPPORTED_LOCALES[0]

  const flush = (previous: LocaleCode) => {
    const code = desired.current
    if (writing.current || !code) return
    writing.current = true
    void auth
      .updateLocale(code)
      .then(adopt)
      .catch(() => {
        // Put the language back NOW rather than letting it stand — but only if
        // this is still the choice on screen. The account is the source of
        // truth and useAccountLocale re-asserts it on the next load, so leaving
        // a failed pick in place would show the new language until some later
        // mount silently reverted it: a change the user believed had stuck,
        // undone with no event they could connect it to. Reverting immediately
        // at least makes the failure visible where the action was taken.
        if (desired.current === code) void i18n.changeLanguage(previous)
      })
      .finally(() => {
        writing.current = false
        if (desired.current !== code) flush(code)
      })
  }

  const choose = (code: LocaleCode) => {
    const previous = (i18n.resolvedLanguage ?? i18n.language) as LocaleCode
    void i18n.changeLanguage(code)
    if (!user) return
    // `user.locale` is stale while a write is in flight, so it can only answer
    // "already stored" when nothing is pending.
    if (!writing.current && user.locale === code) return
    desired.current = code
    flush(previous)
  }

  return { current, choose }
}

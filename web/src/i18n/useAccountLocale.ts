import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { useCurrentUser } from '../auth/AuthProvider'

/**
 * Makes the account's stored language preference the one the interface follows.
 *
 * Without this there would be two truths about the language: `app_user.locale`
 * decides what the e-mails say, while `localStorage["foldex.locale"]` decides
 * what the screen says — and they only agree on the device where the choice was
 * made. Signing in somewhere else, or on a fresh profile, would show one
 * language and mail another, with nothing on screen explaining which is which.
 *
 * A NULL preference is left alone on purpose. It means "no preference", not
 * "English", so the browser hint keeps deciding — the same rule the backend
 * applies when it picks a locale for a message.
 */
export function useAccountLocale(): void {
  const user = useCurrentUser()
  const { i18n } = useTranslation()
  const preference = user?.locale ?? ''

  useEffect(() => {
    if (!preference) return
    if ((i18n.resolvedLanguage ?? i18n.language) === preference) return
    void i18n.changeLanguage(preference)
  }, [preference, i18n])
}

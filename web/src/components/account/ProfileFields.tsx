import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from 'i18next'
import { useMutation } from '@tanstack/react-query'
import { I } from '../icons'
import { Notice, SectionCard } from './SectionCard'
import * as auth from '../../api/auth'
import { useAuth } from '../../auth/AuthProvider'
import { apiErrorCode as errCode } from '../../lib/apiError'
import { SUPPORTED_LOCALES } from '../../i18n'
import type { AuthUser } from '../../auth/types'

/** Mirrors the backend's maxProfileNameRunes — both caps count CHARACTERS, so
 *  a CJK-heavy name is judged identically on client and server. */
export const MAX_DISPLAY_NAME = 120

/**
 * The two fields the account owner edits about themselves: display name and
 * language.
 *
 * E-mail is deliberately absent: it is the account's identity — login,
 * recovery and invites all key off it — so changing it is a verification flow
 * of its own, not an inline edit. Role is shown in the hero and changes only
 * through administration.
 */
export function ProfileFields({ user }: { user: AuthUser }) {
  const { t } = useTranslation()
  const { adopt } = useAuth()

  const [name, setName] = useState(user.name ?? '')
  // '' is a real, selectable value: "follow my browser". It is what lets
  // someone undo a choice, which a plain list of languages cannot express.
  const [locale, setLocale] = useState(user.locale ?? '')
  const [username, setUsername] = useState(user.username ?? '')
  const [error, setError] = useState('')
  const [ok, setOk] = useState(false)

  const rename = useMutation({
    // locale travels ONLY when it changed. Sending the field on every save
    // would make a plain rename overwrite whatever preference is stored — and
    // if it had been changed from another tab or device since this screen
    // loaded, the rename would silently undo it. Omitting it is what the
    // tri-state on both sides exists for.
    mutationFn: () =>
      auth.updateProfile(
        name,
        locale === (user.locale ?? '') ? undefined : locale,
        username.trim() === (user.username ?? '') ? undefined : username.trim(),
      ),
    onSuccess: (me) => {
      setError('')
      setOk(true)
      adopt(me)
      // The interface follows the choice immediately. Leaving the two out of
      // step would mean the account claims one language while the screen shows
      // another, and the user has no way to tell which the e-mail will use.
      const saved = me.status === 'authenticated' ? me.user.locale : undefined
      if (saved) void i18n.changeLanguage(saved)
    },
    onError: (e) => {
      setOk(false)
      const code = errCode(e)
      if (code === 'invalid_name') setError(t('profile.err_name_too_long'))
      else if (code === 'invalid_locale') setError(t('profile.err_locale_unsupported'))
      else if (code === 'invalid_username') setError(t('profile.err_username_shape'))
      else if (code === 'username_taken') setError(t('profile.err_username_taken'))
      else setError(t('auth_errors.generic'))
    },
  })

  const dirty =
    name.trim() !== (user.name ?? '') ||
    locale !== (user.locale ?? '') ||
    username.trim() !== (user.username ?? '')

  const save = () => {
    setOk(false)
    if (name.trim().length > MAX_DISPLAY_NAME) {
      setError(t('profile.err_name_too_long'))
      return
    }
    rename.mutate()
  }

  return (
    <SectionCard
      icon={I.user}
      title={t('profile.card_title')}
      subtitle={t('profile.card_subtitle')}
    >
      {/*
        The messages come FIRST, above the fields.
        They used to be 12px one-liners below the form, at the same weight as a
        field hint — a refused save and a saved change looked like footnotes.
        Above the fields they are also above the fold on a phone, where the
        button that produced them is the thing being scrolled past.
      */}
      {error && <Notice tone="bad">{error}</Notice>}
      {ok && <Notice tone="ok">{t('profile.saved')}</Notice>}

      <div className="fx-sec-form">
        <label className="fx-field">
          <span className="fx-field-label">{t('profile.name_label')}</span>
          <div className="fx-input">
            <input
              type="text"
              maxLength={MAX_DISPLAY_NAME}
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                setError('')
                setOk(false)
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && dirty && !rename.isPending) {
                  e.preventDefault()
                  save()
                }
              }}
              placeholder={t('profile.name_placeholder')}
              aria-label={t('profile.name_label')}
            />
          </div>
        </label>

        <label className="fx-field">
          <span className="fx-field-label">{t('profile.username_label')}</span>
          <div className="fx-input">
            <input
              type="text"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              maxLength={32}
              value={username}
              onChange={(e) => {
                setUsername(e.target.value)
                setError('')
                setOk(false)
              }}
              placeholder={t('profile.username_placeholder')}
              aria-label={t('profile.username_label')}
            />
          </div>
          <span className="fx-field-hint">{t('profile.username_hint')}</span>
        </label>

        <label className="fx-field">
          <span className="fx-field-label">{t('profile.locale_label')}</span>
          <div className="fx-input">
            <select
              value={locale}
              onChange={(e) => {
                setLocale(e.target.value)
                setError('')
                setOk(false)
              }}
              aria-label={t('profile.locale_label')}
              style={{ width: '100%', border: 0, background: 'transparent', font: 'inherit', color: 'inherit' }}
            >
              <option value="">{t('profile.locale_auto')}</option>
              {SUPPORTED_LOCALES.map(({ code, label, flag }) => (
                <option key={code} value={code}>{flag} {label}</option>
              ))}
            </select>
          </div>
          <span className="fx-field-hint">{t('profile.locale_hint')}</span>
        </label>

        <div className="fx-sec-actions">
          <button
            className="fx-btn fx-btn-primary"
            onClick={save}
            disabled={!dirty || rename.isPending}
          >
            {t('profile.save_action')}
          </button>
        </div>
      </div>

      {/* Why the e-mail is not on the form. Info rather than a field hint: it
          answers "where do I change it", which is a question about the whole
          card, not about the field above it. */}
      <Notice tone="info">{t('account.email_fixed_hint')}</Notice>
    </SectionCard>
  )
}

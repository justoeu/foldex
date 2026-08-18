import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from 'i18next'
import { useMutation } from '@tanstack/react-query'
import { Icon, I } from './icons'
import { useConfirm } from './ConfirmDialog'
import * as auth from '../api/auth'
import { http } from '../api/client'
import { useAuth } from '../auth/AuthProvider'
import { apiErrorCode as errCode } from '../lib/apiError'
import { SUPPORTED_LOCALES } from '../i18n'

/** Mirrors the backend's maxProfileNameRunes — both caps count CHARACTERS, so
 *  a CJK-heavy name is judged identically on client and server. */
export const MAX_DISPLAY_NAME = 120

/** "Valmir Justo" → "VJ"; "grace" → "G"; "" → "?" (the placeholder keeps the
 *  avatar a stable square instead of collapsing when the name is empty). */
export function initialsOf(name: string, email: string): string {
  const source = name.trim() || email
  const parts = source.split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0]!.slice(0, 1).toUpperCase()
  return (parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase()
}

/**
 * The signed-in user's own profile: display card (avatar, name, e-mail, role)
 * plus the self-service actions — rename, sign out, sign out everywhere.
 *
 * E-mail is read-only on purpose: it is the account's identity (login,
 * recovery, invites key off it), so changing it is a verification flow of its
 * own, not an inline edit. Role/status are shown for orientation only — they
 * change exclusively through the administration surface.
 */
export function ProfileSection({ onAfterSignOut }: { onAfterSignOut?: () => void }) {
  const { t } = useTranslation()
  const { session, adopt, signOut } = useAuth()
  const confirmAction = useConfirm()
  const user = session.status === 'authenticated' ? session.user : null

  const [name, setName] = useState(user?.name ?? '')
  // '' is a real, selectable value: "follow my browser". It is what lets
  // someone undo a choice, which a plain list of languages cannot express.
  const [locale, setLocale] = useState(user?.locale ?? '')
  const [error, setError] = useState('')
  const [ok, setOk] = useState(false)

  const rename = useMutation({
    // locale travels ONLY when it changed. Sending the field on every save
    // would make a plain rename overwrite whatever preference is stored — and
    // if it had been changed from another tab or device since this screen
    // loaded, the rename would silently undo it. Omitting it is what the
    // tri-state on both sides exists for.
    mutationFn: () =>
      auth.updateProfile(name, locale === (user?.locale ?? '') ? undefined : locale),
    onSuccess: (me) => {
      setError('')
      setOk(true)
      adopt(me)
      // The interface follows the choice immediately. Leaving the two out of
      // step would mean the account claims one language while the screen shows
      // another, and the user has no way to tell which one the e-mail will use.
      const saved = me.status === 'authenticated' ? me.user.locale : undefined
      if (saved) void i18n.changeLanguage(saved)
    },
    onError: (e) => {
      setOk(false)
      const code = errCode(e)
      if (code === 'invalid_name') setError(t('profile.err_name_too_long'))
      else if (code === 'invalid_locale') setError(t('profile.err_locale_unsupported'))
      else setError(t('auth_errors.generic'))
    },
  })

  const save = () => {
    setOk(false)
    if (name.trim().length > MAX_DISPLAY_NAME) {
      setError(t('profile.err_name_too_long'))
      return
    }
    rename.mutate()
  }

  const signOutNow = async () => {
    await signOut()
    onAfterSignOut?.()
  }

  // logout-all kills every session INCLUDING this one; the local state then
  // has to follow, the same way a password change elsewhere signs this tab
  // out. Destructive confirmation because it signs out every other device.
  const signOutEverywhere = async () => {
    const yes = await confirmAction({
      title: t('profile.logout_all_title'),
      message: t('profile.logout_all_message'),
      destructive: true,
    })
    if (!yes) return
    try {
      await http.post('/api/auth/logout-all')
    } finally {
      await signOutNow()
    }
  }

  if (!user) return null
  const dirty = name.trim() !== (user.name ?? '') || locale !== (user.locale ?? '')

  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 14, padding: 18 }}>
        <h3 className="fx-card-title" style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon d={I.user} size={15} /> {t('profile.title')}
        </h3>

        <div style={{ display: 'flex', gap: 14, alignItems: 'center' }}>
          <span className="fx-avatar" aria-hidden="true">{initialsOf(user.name, user.email)}</span>
          <span style={{ minWidth: 0 }}>
            <strong style={{ fontSize: 14, display: 'block', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {user.name || user.email}
            </strong>
            <span style={{ fontSize: 12, color: 'var(--fx-ink-3)', display: 'block' }}>
              {user.email} · {t(`admin.role_${user.role}`)}
            </span>
          </span>
        </div>

        <label className="fx-field" style={{ margin: 0 }}>
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
          <span className="fx-field-hint">{t('profile.name_hint')}</span>
        </label>

        <label className="fx-field" style={{ margin: 0 }}>
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

        {error && (
          <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4 }}>
            <Icon d={I.alert} size={12} /> {error}
          </div>
        )}
        {ok && (
          <div style={{ fontSize: 11, color: 'var(--fx-success)', display: 'flex', alignItems: 'center', gap: 4 }}>
            <Icon d={I.check} size={12} /> {t('profile.saved')}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          <button
            className="fx-confirm-btn fx-confirm-btn-primary"
            onClick={save}
            disabled={!dirty || rename.isPending}
          >
            <Icon d={I.check} size={13} stroke={2.2} /> {t('profile.save_action')}
          </button>
          <button className="fx-confirm-btn" onClick={() => void signOutNow()}>
            <Icon d={I.arrowR} size={13} stroke={2} /> {t('auth.sign_out')}
          </button>
          <button className="fx-confirm-btn fx-confirm-btn-warn" onClick={() => void signOutEverywhere()}>
            <Icon d={I.users} size={13} stroke={2} /> {t('profile.logout_all_action')}
          </button>
        </div>
      </div>
    </section>
  )
}

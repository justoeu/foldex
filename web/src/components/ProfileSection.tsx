import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation } from '@tanstack/react-query'
import { Icon, I } from './icons'
import { useConfirm } from './ConfirmDialog'
import * as auth from '../api/auth'
import { http } from '../api/client'
import { useAuth } from '../auth/AuthProvider'
import { apiErrorCode as errCode } from '../lib/apiError'

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
  const [error, setError] = useState('')
  const [ok, setOk] = useState(false)

  const rename = useMutation({
    mutationFn: () => auth.updateProfile(name),
    onSuccess: (me) => {
      setError('')
      setOk(true)
      adopt(me)
    },
    onError: (e) => {
      setOk(false)
      setError(errCode(e) === 'invalid_name' ? t('profile.err_name_too_long') : t('auth_errors.generic'))
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
  const dirty = name.trim() !== (user.name ?? '')

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

        {error && (
          <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4 }}>
            <Icon d={I.alert} size={12} /> {error}
          </div>
        )}
        {ok && (
          <div style={{ fontSize: 11, color: 'var(--fx-ok, #10B981)', display: 'flex', alignItems: 'center', gap: 4 }}>
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

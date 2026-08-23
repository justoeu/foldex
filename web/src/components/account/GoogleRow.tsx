import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { I } from '../icons'
import { Notice, SectionRow } from './SectionCard'
import { GoogleButton } from '../auth/GoogleButton'
import { PasswordInput } from '../PasswordInput'
import { GoogleLinkDialog } from './GoogleLinkDialog'
import { accountErrorMessage } from './accountErrors'
import * as auth from '../../api/auth'
import { useAuth } from '../../auth/AuthProvider'
import { canMailStepUpCode, hasSecondFactor, type AuthUser } from '../../auth/types'

/**
 * The Google identity linked to this account.
 *
 * It sits directly under the password row, and that adjacency is load-bearing
 * rather than cosmetic: an account converted to Google-only cannot unlink until
 * it has a password again, so "set a password" and "disconnect Google" have to
 * be visible together for the ordering to be obvious instead of discovered
 * through a 409.
 */
export function GoogleRow({ user }: { user: AuthUser }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const { reload } = useAuth()

  const identities = useQuery({ queryKey: ['identities'], queryFn: auth.listIdentities })
  const google = identities.data?.find((i) => i.provider === 'google')
  const hasPassword = user.has_password

  const [unlinkPassword, setUnlinkPassword] = useState('')
  const [linkOpen, setLinkOpen] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)

  const needsStepUp = hasSecondFactor(user)
  const canMailCode = canMailStepUpCode(user)

  async function disconnect() {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await auth.unlinkGoogle(unlinkPassword)
      setUnlinkPassword('')
      setNotice(t('account.google_disconnected'))
      await qc.invalidateQueries({ queryKey: ['identities'] })
      await reload()
    } catch (e) {
      setError(accountErrorMessage(e, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <SectionRow
      icon={I.globe}
      name={t('account.google_label')}
      hint={
        google
          ? t('account.google_connected', { email: google.email_at_link || '' })
          : t('account.google_not_connected')
      }
      tone={google ? 'on' : undefined}
      state={{
        label: google ? t('account.state_linked') : t('account.state_unset'),
        on: Boolean(google),
      }}
      /* Not a button but an explanation: the server refuses to remove the last
         credential, so the way out is the password row above, not this one. */
      note={google && !hasPassword ? t('account.google_only_note') : undefined}
      action={
        !google ? (
          <div className="fx-authfield">
            <GoogleButton
              purpose="link"
              label={t('account.connect_google')}
              onClick={() => setLinkOpen(true)}
            />
          </div>
        ) : undefined
      }
    >
      {notice && <Notice tone="ok">{notice}</Notice>}

      {google && hasPassword && (
        <>
          {error && <Notice tone="bad">{error}</Notice>}
          <label className="fx-field">
            <span className="fx-field-label">{t('account.current_password')}</span>
            <PasswordInput
              className="fx-input"
              autoComplete="current-password"
              value={unlinkPassword}
              onChange={(e) => setUnlinkPassword(e.target.value)}
            />
          </label>
          <div className="fx-sec-actions">
            <button
              className="fx-btn fx-btn-danger"
              disabled={busy || !unlinkPassword}
              onClick={() => void disconnect()}
            >
              {t('account.disconnect_google')}
            </button>
          </div>
        </>
      )}

      {linkOpen && (
        <GoogleLinkDialog
          hasSecondFactor={needsStepUp}
          canMailCode={canMailCode}
          onClose={() => setLinkOpen(false)}
        />
      )}
    </SectionRow>
  )
}

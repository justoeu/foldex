import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from './icons'
import { OtpInput, OTP_LENGTH } from './auth/OtpInput'
import { GoogleButton } from './auth/GoogleButton'
import { PasswordStrength } from './PasswordStrength'
import { listIdentities, setPassword as apiSetPassword, unlinkGoogle } from '../api/auth'
import { apiErrorCode as errCode } from '../lib/apiError'
import { useAuth } from '../auth/AuthProvider'
import { MIN_PASSWORD_LEN } from '../auth/types'

/**
 * Sign-in methods for the caller's own account: the Google link, and the
 * password.
 *
 * The two are one section rather than two because they constrain each other,
 * and separating them would let the UI offer a dead end. An account converted
 * to Google-only cannot unlink until it has a password again — so "set a
 * password" and "disconnect Google" have to be visible together for the
 * ordering to be obvious rather than discovered through a 409.
 */
export function AccountSection() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const { session, reload } = useAuth()

  const identities = useQuery({ queryKey: ['identities'], queryFn: listIdentities })
  const [password, setPasswordValue] = useState('')
  const [confirm, setConfirm] = useState('')
  const [code, setCode] = useState('')
  const [unlinkPassword, setUnlinkPassword] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)

  const user = session.status === 'authenticated' ? session.user : null
  const googleEnabled = session.status !== 'loading' && session.features.google_oauth
  const google = identities.data?.find((i) => i.provider === 'google')
  const hasPassword = user?.has_password ?? true
  const totpEnabled = user?.totp_enabled ?? false

  async function addPassword() {
    if (password !== confirm) {
      setError(t('auth_errors.password_mismatch'))
      return
    }
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await apiSetPassword(password, code)
      setPasswordValue('')
      setConfirm('')
      setCode('')
      setNotice(t('account.password_set_done'))
      // has_password lives on the session, outside the query cache, so
      // invalidating identities alone would leave the section still offering
      // to set a password that now exists.
      await reload()
    } catch (err) {
      setError(messageFor(err, t))
    } finally {
      setBusy(false)
    }
  }

  async function disconnect() {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await unlinkGoogle(unlinkPassword)
      setUnlinkPassword('')
      setNotice(t('account.google_disconnected'))
      await qc.invalidateQueries({ queryKey: ['identities'] })
    } catch (err) {
      setError(messageFor(err, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3
          className="fx-card-title"
          style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}
        >
          <Icon d={I.user} size={15} /> {t('account.section_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>
          {t('account.section_desc')}
        </p>

        {error && (
          <div className="fx-inline-error" role="alert" style={{ fontSize: 12 }}>
            {error}
          </div>
        )}
        {notice && (
          <div style={{ fontSize: 12, color: 'var(--fx-ink-2)' }} role="status">
            {notice}
          </div>
        )}

        {/* ── Password ─────────────────────────────────────────────── */}
        <div style={{ fontSize: 12, display: 'flex', alignItems: 'center', gap: 6 }}>
          <Icon d={hasPassword ? I.check : I.info} size={13} />
          {hasPassword ? t('account.password_on') : t('account.password_off')}
        </div>

        {!hasPassword && (
          <>
            <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>
              {t('account.password_why')}
            </p>
            <label className="fx-field" style={{ margin: 0 }}>
              <span className="fx-field-label">{t('account.new_password')}</span>
              <input
                className="fx-input"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPasswordValue(e.target.value)}
              />
            </label>
            <PasswordStrength value={password} />
            <label className="fx-field" style={{ margin: 0 }}>
              <span className="fx-field-label">{t('account.confirm_password')}</span>
              <input
                className="fx-input"
                type="password"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
              />
            </label>
            {/* With no current password to prove, the authenticator is the only
                step-up available — and this credential outlives the session
                presenting the request, so a cookie alone is too weak a proof. */}
            {totpEnabled && (
              <label className="fx-field" style={{ margin: 0 }}>
                <span className="fx-field-label">{t('account.current_code')}</span>
                <div className="fx-auth" style={{ position: 'static', padding: 0, background: 'none' }}>
                  <OtpInput value={code} onChange={setCode} disabled={busy} />
                </div>
              </label>
            )}
            <div>
              <button
                className="fx-btn fx-btn-primary"
                disabled={
                  busy ||
                  password.length < MIN_PASSWORD_LEN ||
                  !confirm ||
                  (totpEnabled && code.length < OTP_LENGTH)
                }
                onClick={() => void addPassword()}
              >
                {t('account.set_password')}
              </button>
            </div>
          </>
        )}

        {/* ── Google ───────────────────────────────────────────────── */}
        {googleEnabled && (
          <div style={{ borderTop: '1px solid var(--fx-line)', paddingTop: 12, display: 'grid', gap: 10 }}>
            <div style={{ fontSize: 12, display: 'flex', alignItems: 'center', gap: 6 }}>
              <Icon d={google ? I.check : I.info} size={13} />
              {google
                ? t('account.google_connected', { email: google.email_at_link || '' })
                : t('account.google_not_connected')}
            </div>

            {!google && (
              <div className="fx-auth" style={{ position: 'static', padding: 0, background: 'none' }}>
                <GoogleButton purpose="link" label={t('account.connect_google')} />
              </div>
            )}

            {google && !hasPassword && (
              <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>
                {t('account.google_only_note')}
              </p>
            )}

            {google && hasPassword && (
              <>
                <label className="fx-field" style={{ margin: 0 }}>
                  <span className="fx-field-label">{t('account.current_password')}</span>
                  <input
                    className="fx-input"
                    type="password"
                    autoComplete="current-password"
                    value={unlinkPassword}
                    onChange={(e) => setUnlinkPassword(e.target.value)}
                  />
                </label>
                <div>
                  <button
                    className="fx-btn"
                    disabled={busy || !unlinkPassword}
                    onClick={() => void disconnect()}
                  >
                    {t('account.disconnect_google')}
                  </button>
                </div>
              </>
            )}
          </div>
        )}
      </div>
    </section>
  )
}

function messageFor(err: unknown, t: (k: string, o?: Record<string, unknown>) => string): string {
  switch (errCode(err)) {
    case 'invalid_credentials':
      return t('account.wrong_password')
    case 'password_required':
      // The 409 the server answers when unlinking would strip the last
      // credential. The UI already hides that button, so reaching this means a
      // password was removed in another tab.
      return t('account.password_required_first')
    case 'password_exists':
      return t('account.password_already_set')
    case 'invalid_code':
      return t('auth_errors.invalid_code')
    case 'password_too_short':
      return t('auth_errors.password_too_short', { count: MIN_PASSWORD_LEN })
    case 'not_linked':
      return t('account.google_not_connected')
    case 'too_many_attempts':
      return t('auth_errors.too_many_attempts')
    default:
      return t('auth_errors.generic')
  }
}

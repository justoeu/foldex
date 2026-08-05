import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { errorCode, errorStatus, resetPassword } from '../../api/auth'
import { useAuth } from '../../auth/AuthProvider'
import { MIN_PASSWORD_LEN } from '../../auth/types'
import { PasswordStrength } from '../PasswordStrength'
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell'

/**
 * Chooses a new password from a reset link.
 *
 * On success the user is signed in directly — they have proven the mailbox AND
 * chosen a password, which is more than the login screen asks for. An account
 * with a second factor is diverted by the server into the code screen instead;
 * this component does not special-case that, because `adopt` handles every
 * shape the credential endpoints return.
 */
export function ResetScreen({ token, onGiveUp }: { token: string; onGiveUp: () => void }) {
  const { t } = useTranslation()
  const { adopt } = useAuth()
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const mismatch = confirm.length > 0 && password !== confirm

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (busy) return
    if (password !== confirm) {
      setError(t('auth_errors.password_mismatch'))
      return
    }
    setBusy(true)
    setError('')
    try {
      adopt(await resetPassword(token, password))
    } catch (err) {
      const code = errorCode(err)
      if (code === 'reset_invalid') setError(t('auth_reset.link_invalid'))
      else if (code === 'password_too_short') setError(t('auth_errors.password_too_short', { count: MIN_PASSWORD_LEN }))
      else if (code === 'password_too_long') setError(t('auth_errors.password_too_long'))
      else if (code === 'too_many_attempts') setError(t('auth_errors.too_many_attempts'))
      else if (errorStatus(err) === 0) setError(t('auth_errors.network'))
      else setError(t('auth_errors.generic'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthShell
      kicker={t('auth_reset.kicker')}
      title={t('auth_reset.title')}
      subtitle={t('auth_reset.subtitle')}
    >
      <form className="fx-auth-form" onSubmit={onSubmit} noValidate>
        <AuthError message={error} />

        <AuthField id="fx-reset-password" label={t('auth_reset.new_password')}>
          <input
            id="fx-reset-password"
            className="fx-auth-input"
            type="password"
            name="new-password"
            autoComplete="new-password"
            autoFocus
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </AuthField>
        <PasswordStrength value={password} />

        <AuthField id="fx-reset-confirm" label={t('auth_reset.confirm_password')}>
          <input
            id="fx-reset-confirm"
            className={'fx-auth-input' + (mismatch ? ' fx-auth-input-invalid' : '')}
            type="password"
            name="confirm-password"
            autoComplete="new-password"
            required
            aria-invalid={mismatch || undefined}
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </AuthField>

        <AuthSubmit busy={busy} disabled={!password || mismatch}>
          {t('auth_reset.submit')}
        </AuthSubmit>

        <div className="fx-auth-alt">
          <button type="button" className="fx-auth-link" onClick={onGiveUp}>
            {t('auth_reset.back_to_login')}
          </button>
        </div>
      </form>
    </AuthShell>
  )
}

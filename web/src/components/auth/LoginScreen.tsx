import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { login, errorCode, errorStatus } from '../../api/auth'
import { useAuth } from '../../auth/AuthProvider'
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell'

export function LoginScreen({ onForgotPassword }: { onForgotPassword?: () => void }) {
  const { t } = useTranslation()
  const { adopt } = useAuth()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      adopt(await login(email, password))
    } catch (err) {
      setError(messageFor(err, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthShell kicker={t('auth_login.kicker')} title={t('auth_login.title')} subtitle={t('auth_login.subtitle')}>
      <form className="fx-auth-form" onSubmit={onSubmit} noValidate>
        <AuthError message={error} />

        <AuthField id="fx-login-email" label={t('auth.email')}>
          <input
            id="fx-login-email"
            className="fx-auth-input"
            type="email"
            name="email"
            autoComplete="username"
            autoFocus
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </AuthField>

        <AuthField id="fx-login-password" label={t('auth.password')}>
          <input
            id="fx-login-password"
            className="fx-auth-input"
            type="password"
            name="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </AuthField>

        <AuthSubmit busy={busy}>{t('auth_login.submit')}</AuthSubmit>

        {onForgotPassword && (
          <div className="fx-auth-alt">
            <button type="button" className="fx-auth-link" onClick={onForgotPassword}>
              {t('auth_login.forgot')}
            </button>
          </div>
        )}
      </form>
    </AuthShell>
  )
}

/**
 * Maps an error onto copy.
 *
 * Every credential failure — unknown address, wrong password, disabled account
 * — arrives as the same `invalid_credentials` code, and the UI must keep it
 * that way. Being more helpful here ("no account with that e-mail") would hand
 * back exactly the account-enumeration oracle the backend spends a dummy bcrypt
 * hash and a 250 ms duration floor to remove.
 */
function messageFor(err: unknown, t: (k: string, o?: Record<string, unknown>) => string): string {
  const code = errorCode(err)
  if (code === 'too_many_attempts') return t('auth_errors.too_many_attempts')
  if (code === 'invalid_credentials') return t('auth_errors.invalid_credentials')
  if (errorStatus(err) === 0) return t('auth_errors.network')
  return t('auth_errors.generic')
}

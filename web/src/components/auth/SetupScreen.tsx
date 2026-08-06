import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { bootstrap, errorCode, errorStatus } from '../../api/auth'
import { useAuth } from '../../auth/AuthProvider'
import { PasswordStrength } from '../PasswordStrength'
import { MIN_PASSWORD_LEN } from '../../auth/types'
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell'

/** Mirrors auth.MinPasswordLen; the backend is the authority. */

export function SetupScreen() {
  const { t } = useTranslation()
  const { adopt } = useAuth()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const mismatch = confirm.length > 0 && password !== confirm
  const tooShort = password.length > 0 && password.length < MIN_PASSWORD_LEN

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (busy) return
    // Client-side checks are guidance only — the backend enforces the real
    // policy. They exist to catch a typo before it costs a round-trip.
    if (password !== confirm) {
      setError(t('auth_errors.password_mismatch'))
      return
    }
    setBusy(true)
    setError('')
    try {
      adopt(await bootstrap(email, name, password))
    } catch (err) {
      setError(messageFor(err, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthShell
      kicker={t('auth_setup.kicker')}
      title={t('auth_setup.title')}
      subtitle={t('auth_setup.subtitle')}
    >
      <form className="fx-auth-form" onSubmit={onSubmit} noValidate>
        <AuthError message={error} />

        <AuthField id="fx-setup-name" label={t('auth.name')}>
          <input
            id="fx-setup-name"
            className="fx-auth-input"
            type="text"
            autoComplete="name"
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </AuthField>

        <AuthField id="fx-setup-email" label={t('auth.email')}>
          <input
            id="fx-setup-email"
            className="fx-auth-input"
            type="email"
            autoComplete="username"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </AuthField>

        <AuthField
          id="fx-setup-password"
          label={t('auth.password')}
          hint={t('auth.password_min', { count: MIN_PASSWORD_LEN })}
        >
          <input
            id="fx-setup-password"
            className="fx-auth-input"
            type="password"
            autoComplete="new-password"
            required
            aria-invalid={tooShort || undefined}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </AuthField>
        <PasswordStrength value={password} />

        <AuthField id="fx-setup-confirm" label={t('auth.confirm_password')}>
          <input
            id="fx-setup-confirm"
            className="fx-auth-input"
            type="password"
            autoComplete="new-password"
            required
            aria-invalid={mismatch || undefined}
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </AuthField>
        {mismatch ? <p className="fx-auth-hint fx-auth-hint-error">{t('auth_errors.password_mismatch')}</p> : null}

        <AuthSubmit busy={busy}>{t('auth_setup.submit')}</AuthSubmit>
      </form>
    </AuthShell>
  )
}

function messageFor(err: unknown, t: (k: string, o?: Record<string, unknown>) => string): string {
  switch (errorCode(err)) {
    case 'already_configured':
      // A second operator got there first. Nothing to retry — reloading shows
      // the login screen.
      return t('auth_errors.already_configured')
    case 'email_taken':
      return t('auth_errors.email_taken')
    case 'invalid_email':
      return t('auth_errors.invalid_email')
    case 'password_too_short':
      return t('auth_errors.password_too_short', { count: MIN_PASSWORD_LEN })
    case 'password_too_long':
      return t('auth_errors.password_too_long')
    default:
      return errorStatus(err) === 0 ? t('auth_errors.network') : t('auth_errors.generic')
  }
}

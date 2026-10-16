import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { bootstrap } from '../../api/auth'
import { apiErrorCode, apiErrorStatus } from '../../lib/apiError'
import { useAuth } from '../../auth/AuthProvider'
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell'
import { PasswordBlock } from './PasswordBlock'
import { passwordGateLen, usePasswordFloor } from '../../hooks/useInstancePolicy'

export function SetupScreen() {
  const { t } = useTranslation()
  const { adopt } = useAuth()
  const minLen = usePasswordFloor()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const mismatch = confirm.length > 0 && password !== confirm
  const tooShort = password.length > 0 && password.length < passwordGateLen(minLen)

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
      setError(messageFor(err, t, minLen))
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

        <PasswordBlock
          passwordId="fx-setup-password"
          confirmId="fx-setup-confirm"
          password={password}
          confirm={confirm}
          minLen={minLen}
          passwordInvalid={tooShort}
          showMismatchHint
          onPasswordChange={setPassword}
          onConfirmChange={setConfirm}
        />

        <AuthSubmit busy={busy} disabled={tooShort || mismatch}>{t('auth_setup.submit')}</AuthSubmit>
      </form>
    </AuthShell>
  )
}

function messageFor(
  err: unknown,
  t: (k: string, o?: Record<string, unknown>) => string,
  minLen: number,
): string {
  switch (apiErrorCode(err)) {
    case 'already_configured':
      // A second operator got there first. Nothing to retry — reloading shows
      // the login screen.
      return t('auth_errors.already_configured')
    case 'email_taken':
      return t('auth_errors.email_taken')
    case 'invalid_email':
      return t('auth_errors.invalid_email')
    case 'password_too_short':
      return t('auth_errors.password_too_short', { count: minLen })
    case 'password_too_long':
      return t('auth_errors.password_too_long')
    default:
      return (apiErrorStatus(err) ?? 0) === 0 ? t('auth_errors.network') : t('auth_errors.generic')
  }
}

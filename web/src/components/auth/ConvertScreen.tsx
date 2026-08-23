import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { convertToGoogle, errorCode, errorStatus } from '../../api/auth'
import { useAuth } from '../../auth/AuthProvider'
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell'
import { PasswordInput } from '../PasswordInput'

/**
 * The account-portability step: someone signed in with Google using an address
 * that already belongs to a PASSWORD account.
 *
 * The screen exists because neither of the two obvious answers is right.
 * Signing them in would mean a matching e-mail address is enough to take over
 * an account — the address is not a secret, and anyone can put one in a Google
 * profile. Refusing outright would strand a real user who simply wants to stop
 * typing a password.
 *
 * So it asks for the CURRENT PASSWORD. That makes conversion something the
 * account's owner performs, not something a mailbox grants — deliberately
 * stricter than password reset, which does hand the account to whoever controls
 * the address. The accepted consequence is that "I forgot my password, let me
 * use Google" does not work: reset first, convert after.
 *
 * The copy is blunt about the password being retired, because it is not
 * reversible from this screen: afterwards the only way back to a password is
 * Settings → set a password, or an administrator.
 */
export function ConvertScreen({ email }: { email: string }) {
  const { t } = useTranslation()
  const { adopt, reload, signOut } = useAuth()
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      // The response is a full session payload OR another pending state: an
      // account with an authenticator still owes a code, because conversion
      // proves one factor and never two. Handing it to `adopt` is what routes
      // both outcomes without this screen knowing which happened.
      adopt(await convertToGoogle(password))
    } catch (err) {
      setError(messageFor(err, t))
      setPassword('')
      // A spent or expired challenge cannot be retried here: the server has
      // already cleared the pre-auth cookie, and nothing on this screen can
      // mint another. Re-probing drops the user back to the login form, the
      // only place the flow can start again.
      const c = errorCode(err)
      if (c === 'challenge_invalid' || c === 'too_many_attempts') void reload()
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthShell
      kicker={t('auth_convert.kicker')}
      title={t('auth_convert.title')}
      subtitle={t('auth_convert.subtitle', { email })}
    >
      <form className="fx-auth-form" onSubmit={onSubmit} noValidate>
        <AuthError message={error} />

        <p className="fx-auth-notice" role="note">
          {t('auth_convert.warning')}
        </p>

        <AuthField id="fx-convert-password" label={t('auth_convert.current_password')}>
          <PasswordInput
            id="fx-convert-password"
            className="fx-auth-input"
            name="password"
            autoComplete="current-password"
            autoFocus
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </AuthField>

        <AuthSubmit busy={busy} disabled={!password}>
          {t('auth_convert.submit')}
        </AuthSubmit>

        <div className="fx-auth-alt">
          {/* signOut, NOT reload. The pre-auth cookie is still live, so
              re-probing /me would answer with this very screen again and the
              button would do nothing. Abandoning the challenge means clearing
              the cookie, which is exactly what logout does — the same way the
              second-factor and enrollment screens back out. */}
          <button type="button" className="fx-auth-link" onClick={() => void signOut()}>
            {t('auth_convert.cancel')}
          </button>
        </div>
      </form>
    </AuthShell>
  )
}

function messageFor(err: unknown, t: (k: string) => string): string {
  const code = errorCode(err)
  if (code === 'invalid_credentials') return t('auth_convert.wrong_password')
  if (code === 'oauth_already_linked') return t('auth_errors.oauth_already_linked')
  if (code === 'too_many_attempts') return t('auth_otp.locked_out')
  if (code === 'challenge_invalid') return t('auth_otp.expired')
  if (errorStatus(err) === 0) return t('auth_errors.network')
  return t('auth_errors.generic')
}

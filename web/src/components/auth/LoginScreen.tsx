import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { login, errorCode, errorStatus } from '../../api/auth'
import { useAuth } from '../../auth/AuthProvider'
import { urlTokens } from '../../auth/authUrl'
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell'
import { GoogleButton, AuthDivider } from './GoogleButton'
import { PasswordInput } from '../PasswordInput'

// Remembering the address is a CONVENIENCE and nothing more: the password is
// never stored, and neither is anything that could stand in for one. It lives
// in localStorage rather than on the account for the obvious reason — the whole
// point is to have it before anyone is signed in.
//
// Unchecking must ERASE, not merely stop writing. A box the user unticks while
// looking at their own address, that then hands the same address back on the
// next visit, has done the opposite of what it says.
const REMEMBER_KEY = 'foldex.auth.email'

function readRemembered(): string {
  try {
    return localStorage.getItem(REMEMBER_KEY) ?? ''
  } catch {
    return '' // storage unavailable (private window, blocked site data)
  }
}

export function forgetRememberedEmail() {
  try {
    localStorage.removeItem(REMEMBER_KEY)
  } catch {
    /* storage unavailable */
  }
}

export function LoginScreen({ onForgotPassword }: { onForgotPassword?: () => void }) {
  const { t } = useTranslation()
  const { adopt, session } = useAuth()
  const [remembered] = useState(readRemembered)
  const [email, setEmail] = useState(remembered)
  const [remember, setRemember] = useState(remembered !== '')
  const [password, setPassword] = useState('')
  // Seeded from the URL because a FAILED Google round-trip comes back here as a
  // plain navigation with no response body to inspect: ?oauth_error= is the
  // only thing distinguishing "you cancelled" from "that account is not
  // linked", and without it the user would land on a blank login form with no
  // explanation for why nothing happened.
  const [error, setError] = useState(oauthErrorMessage(urlTokens.oauthError, t))
  const [busy, setBusy] = useState(false)
  const googleEnabled = session.status !== 'loading' && session.features.google_oauth

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const me = await login(email, password)
      // Written only after the credentials were ACCEPTED. Remembering a typo the
      // user is still correcting would hand the typo back next time.
      try {
        if (remember) localStorage.setItem(REMEMBER_KEY, email)
      } catch {
        /* storage unavailable — the sign-in itself is unaffected */
      }
      adopt(me)
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

        {googleEnabled && (
          <>
            <GoogleButton purpose="login" />
            <AuthDivider />
          </>
        )}

        <AuthField id="fx-login-email" label={t('auth.identifier')}>
          {/*
            `type="text"`, not `type="email"`. The field now accepts a username
            too, and an e-mail input refuses to submit anything without an `@` —
            the browser would block a perfectly valid sign-in with a validation
            bubble the server never gets to answer.
          */}
          <input
            id="fx-login-email"
            className="fx-auth-input"
            type="text"
            name="username"
            autoComplete="username"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            autoFocus
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </AuthField>

        <AuthField id="fx-login-password" label={t('auth.password')}>
          <PasswordInput
            id="fx-login-password"
            className="fx-auth-input"
            name="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </AuthField>

        <label className="fx-auth-check">
          <input
            type="checkbox"
            checked={remember}
            onChange={(e) => {
              setRemember(e.target.checked)
              // Erase on the UNTICK, not on a later successful sign-in. Someone
              // who unticks and walks away — or unticks and then fails to sign
              // in — would otherwise leave the address exactly where they just
              // asked for it not to be.
              if (!e.target.checked) forgetRememberedEmail()
            }}
          />
          <span>{t('auth.remember_email')}</span>
        </label>

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

/**
 * Turns the callback's `?oauth_error=` marker into copy.
 *
 * `not_linked` is deliberately vague — "we could not sign you in with that
 * account". The server answers the same code for an unknown address, a
 * disabled account and one that exists but has no Google link, precisely so
 * the redirect cannot be used to test whether an address has an account here.
 * Spelling out which case it was would rebuild that oracle in the UI.
 */
function oauthErrorMessage(
  code: string | undefined,
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  switch (code) {
    case undefined:
    case '':
      return ''
    case 'cancelled':
      return t('auth_errors.oauth_cancelled')
    case 'not_linked':
      return t('auth_errors.oauth_not_linked')
    case 'email_unverified':
      return t('auth_errors.oauth_email_unverified')
    case 'already_linked':
      return t('auth_errors.oauth_already_linked')
    case 'invite_email_mismatch':
      return t('auth_errors.oauth_invite_mismatch')
    case 'invite_invalid':
      return t('auth_errors.oauth_invite_invalid')
    case 'oauth_disabled':
      return t('auth_errors.oauth_disabled')
    default:
      return t('auth_errors.oauth_failed')
  }
}

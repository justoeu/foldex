import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { acceptInvite, lookupInvite, errorCode, errorStatus, type InvitePreview } from '../../api/auth'
import { useAuth } from '../../auth/AuthProvider'
import { PasswordStrength } from '../PasswordStrength'
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell'
import { AuthDivider, GoogleButton } from './GoogleButton'
import { PasswordInput } from '../PasswordInput'

const MIN_PASSWORD = 8
type LookupState = 'loading' | 'ready' | 'invalid' | 'failed'

/**
 * Accepts an invitation.
 *
 * The e-mail is read-only and comes from the SERVER's view of the token, never
 * from anything the visitor can type. Letting the address be edited here would
 * turn a `user` invitation into "create an account for any address I like",
 * which is the whole reason self-signup is off.
 */
export function InviteScreen({ token, onGiveUp }: { token: string; onGiveUp: () => void }) {
  const { t } = useTranslation()
  const { adopt, session } = useAuth()
  const [preview, setPreview] = useState<InvitePreview | null>(null)
  const [lookupState, setLookupState] = useState<LookupState>('loading')
  const [lookupError, setLookupError] = useState<'network' | 'generic'>('network')
  const [lookupAttempt, setLookupAttempt] = useState(0)
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [oauthBusy, setOAuthBusy] = useState(false)
  const submitting = useRef(false)
  const oauthBusyRef = useRef(false)
  const tokenRef = useRef(token)
  const acceptAbortRef = useRef<AbortController | null>(null)
  tokenRef.current = token
  const googleEnabled = session.status !== 'loading' && session.features.google_oauth

  useEffect(() => {
    acceptAbortRef.current?.abort()
    acceptAbortRef.current = null
    submitting.current = false
    oauthBusyRef.current = false
    setBusy(false)
    setOAuthBusy(false)
    setName('')
    setPassword('')
    setConfirm('')
    setError('')
  }, [token])

  useEffect(() => () => acceptAbortRef.current?.abort(), [])

  useEffect(() => {
    const controller = new AbortController()
    setPreview(null)
    setLookupState('loading')
    lookupInvite(token, controller.signal)
      .then((p) => {
        if (controller.signal.aborted) return
        setPreview(p)
        setLookupState('ready')
      })
      .catch((err) => {
        if (controller.signal.aborted) return
        // Expired, revoked, already used and never-existed are one
        // indistinguishable 404 by design — so there is exactly one message.
        const code = errorCode(err)
        if (code === 'invite_invalid' || code === 'not_found' || errorStatus(err) === 404) {
          setLookupState('invalid')
          return
        }
        setLookupError(errorStatus(err) === 0 ? 'network' : 'generic')
        setLookupState('failed')
      })
    return () => controller.abort()
  }, [token, lookupAttempt])

  if (lookupState === 'invalid') {
    return (
      <AuthShell title={t('auth_invite.invalid_title')} subtitle={t('auth_invite.invalid_body')}>
        <button type="button" className="fx-auth-submit" onClick={onGiveUp}>
          {t('auth_invite.back_to_login')}
        </button>
      </AuthShell>
    )
  }

  if (lookupState === 'failed') {
    return (
      <AuthShell title={t('auth_invite.title')}>
        <div className="fx-auth-form">
          <AuthError message={t(`auth_errors.${lookupError}`)} />
          <button
            type="button"
            className="fx-auth-submit"
            onClick={() => setLookupAttempt((attempt) => attempt + 1)}
          >
            {t('auth_invite.retry')}
          </button>
        </div>
      </AuthShell>
    )
  }

  if (lookupState === 'loading' || !preview) {
    return (
      <AuthShell title={t('auth_invite.title')}>
        <div className="fx-auth-loading" role="status" aria-live="polite">
          <span className="fx-auth-spinner" aria-hidden="true" />
          {t('auth.loading')}
        </div>
      </AuthShell>
    )
  }

  const mismatch = confirm.length > 0 && password !== confirm

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (submitting.current || oauthBusyRef.current) return
    if (password !== confirm) {
      setError(t('auth_errors.password_mismatch'))
      return
    }
    submitting.current = true
    const submittedToken = token
    const controller = new AbortController()
    acceptAbortRef.current = controller
    setBusy(true)
    setError('')
    try {
      const response = await acceptInvite(token, name, password, controller.signal)
      if (!controller.signal.aborted && tokenRef.current === submittedToken) adopt(response)
    } catch (err) {
      if (!controller.signal.aborted && tokenRef.current === submittedToken) {
        setError(messageFor(err, t))
      }
    } finally {
      if (acceptAbortRef.current === controller) acceptAbortRef.current = null
      if (tokenRef.current === submittedToken) {
        submitting.current = false
        setBusy(false)
      }
    }
  }

  return (
    <AuthShell
      kicker={t('auth_invite.kicker')}
      title={t('auth_invite.title')}
      subtitle={t('auth_invite.subtitle')}
    >
      <form className="fx-auth-form" onSubmit={onSubmit} noValidate>
        <AuthError message={error} />

        {googleEnabled && (
          <>
            <GoogleButton
              purpose="accept_invite"
              invite={token}
              disabled={busy}
              onBeforeStart={() => {
                if (submitting.current || oauthBusyRef.current) return false
                oauthBusyRef.current = true
                setOAuthBusy(true)
                return true
              }}
              onBusyChange={(pending) => {
                oauthBusyRef.current = pending
                setOAuthBusy(pending)
              }}
            />
            <AuthDivider />
          </>
        )}

        <AuthField id="fx-invite-email" label={t('auth.email')} hint={t('auth_invite.email_locked')}>
          <input
            id="fx-invite-email"
            className="fx-auth-input"
            type="email"
            value={preview.email}
            readOnly
            aria-readonly="true"
          />
        </AuthField>

        <AuthField id="fx-invite-name" label={t('auth.name')}>
          <input
            id="fx-invite-name"
            className="fx-auth-input"
            type="text"
            autoComplete="name"
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </AuthField>

        <AuthField
          id="fx-invite-password"
          label={t('auth.password')}
          hint={t('auth.password_min', { count: MIN_PASSWORD })}
        >
          <PasswordInput
            id="fx-invite-password"
            className="fx-auth-input"
            autoComplete="new-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </AuthField>
        <PasswordStrength value={password} />

        <AuthField id="fx-invite-confirm" label={t('auth.confirm_password')}>
          <PasswordInput
            id="fx-invite-confirm"
            className="fx-auth-input"
            autoComplete="new-password"
            required
            aria-invalid={mismatch || undefined}
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </AuthField>

        <AuthSubmit busy={busy} disabled={oauthBusy}>{t('auth_invite.submit')}</AuthSubmit>
      </form>
    </AuthShell>
  )
}

function messageFor(err: unknown, t: (k: string, o?: Record<string, unknown>) => string): string {
  switch (errorCode(err)) {
    case 'invite_invalid':
      return t('auth_invite.invalid_body')
    case 'email_taken':
      return t('auth_errors.email_taken')
    case 'password_too_short':
      return t('auth_errors.password_too_short', { count: MIN_PASSWORD })
    case 'password_too_long':
      return t('auth_errors.password_too_long')
    case 'too_many_attempts':
      return t('auth_errors.too_many_attempts')
    default:
      return errorStatus(err) === 0 ? t('auth_errors.network') : t('auth_errors.generic')
  }
}

import { useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from './icons'
import { OtpInput, OTP_LENGTH } from './auth/OtpInput'
import { GoogleButton } from './auth/GoogleButton'
import { PasswordStrength } from './PasswordStrength'
import * as auth from '../api/auth'
import { sendStepUpCode } from '../api/twofa'
import { apiErrorCode as errCode } from '../lib/apiError'
import { useAuth } from '../auth/AuthProvider'
import { MIN_PASSWORD_LEN } from '../auth/types'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'

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

  const identities = useQuery({ queryKey: ['identities'], queryFn: auth.listIdentities })
  const [password, setPasswordValue] = useState('')
  const [confirm, setConfirm] = useState('')
  const [code, setCode] = useState('')
  const [unlinkPassword, setUnlinkPassword] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [linkOpen, setLinkOpen] = useState(false)

  const user = session.status === 'authenticated' ? session.user : null
  const googleEnabled = session.status !== 'loading' && session.features.google_oauth
  const google = identities.data?.find((i) => i.provider === 'google')
  const hasPassword = user?.has_password ?? true
  // The AGGREGATE, not the authenticator alone. The server demands a step-up
  // proof from any account holding a second factor (`HasSecondFactor()`), so
  // reading totp_enabled here hid the code field from an account whose only
  // factor is e-mail — leaving the button enabled, the request sent without a
  // code, and a refusal the screen gave the user no way to answer.
  const hasSecondFactor = (user?.totp_enabled ?? false) || (user?.email_2fa_enabled ?? false)
  // Whether a MAILED code is obtainable here. An e-mail-only account has no
  // authenticator to read six digits from, so without this its only proof is a
  // recovery code — a lockout credential spent on an ordinary settings change.
  const canMailCode = user?.email_2fa_enabled ?? false

  async function addPassword() {
    if (password !== confirm) {
      setError(t('auth_errors.password_mismatch'))
      return
    }
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await auth.setPassword(password, code)
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
      await auth.unlinkGoogle(unlinkPassword)
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
            {/* With no current password to prove, a second factor is the only
                step-up available — and this credential outlives the session
                presenting the request, so a cookie alone is too weak a proof. */}
            {hasSecondFactor && (
              <label className="fx-field" style={{ margin: 0 }}>
                <span className="fx-field-label">{t('account.current_code')}</span>
                <div className="fx-auth" style={{ position: 'static', padding: 0, background: 'none' }}>
                  <OtpInput value={code} onChange={setCode} disabled={busy} />
                </div>
                {canMailCode && <MailCodeButton disabled={busy} />}
              </label>
            )}
            <div>
              <button
                className="fx-btn fx-btn-primary"
                disabled={
                  busy ||
                  password.length < MIN_PASSWORD_LEN ||
                  !confirm ||
                  (hasSecondFactor && code.length < OTP_LENGTH)
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
                <GoogleButton
                  purpose="link"
                  label={t('account.connect_google')}
                  onClick={() => setLinkOpen(true)}
                />
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
      {linkOpen && (
        <GoogleLinkDialog
          hasSecondFactor={hasSecondFactor}
          canMailCode={canMailCode}
          onClose={() => setLinkOpen(false)}
        />
      )}
    </section>
  )
}

/**
 * Asks the server to mail a step-up code.
 *
 * The only proof an e-mail-only account can produce here: it has no
 * authenticator to read six digits from, and the alternative — a recovery code —
 * is a lockout credential, too expensive to spend on setting a password.
 *
 * Element-qualified class: `.fx-shell button` is (0,1,1) and out-specifies a
 * bare component class, which would strip this back to plain prose.
 */
function MailCodeButton({ disabled }: { disabled: boolean }) {
  const { t } = useTranslation()
  const [sent, setSent] = useState(false)
  const [busy, setBusy] = useState(false)
  return (
    <span className="fx-field-hint" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
      {sent ? t('twofa.code_sent') : t('twofa.code_accepts_mailed')}
      <button
        type="button"
        className="fx-field-action"
        disabled={disabled || busy}
        onClick={() => {
          setBusy(true)
          sendStepUpCode()
            .then(() => setSent(true))
            .catch(() => setSent(false))
            .finally(() => setBusy(false))
        }}
      >
        {t('twofa.mail_a_code')}
      </button>
    </span>
  )
}

function GoogleLinkDialog({
  hasSecondFactor,
  canMailCode,
  onClose,
}: {
  hasSecondFactor: boolean
  canMailCode: boolean
  onClose: () => void
}) {
  const { t } = useTranslation()
  const dialogRef = useRef<HTMLDivElement>(null)
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [method, setMethod] = useState<'totp' | 'recovery'>('totp')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  useEscape(onClose)
  useFocusTrap(dialogRef, true)

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const redirectURL = await auth.beginGoogleLink(password, hasSecondFactor ? code : '')
      auth.navigateToOAuth(redirectURL)
    } catch (err) {
      setError(messageFor(err, t))
      setCode('')
    } finally {
      setBusy(false)
    }
  }

  const codeReady =
    !hasSecondFactor || (method === 'totp' ? code.length === OTP_LENGTH : code.trim() !== '')
  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={t('account.link_title')}
    >
      <form className="fx-modal fx-confirm" onSubmit={(e) => void submit(e)}>
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker fx-modal-kicker-info">{t('account.link_kicker')}</div>
            <h2 className="fx-modal-title">{t('account.link_title')}</h2>
          </div>
          <button type="button" className="fx-confirm-x" onClick={onClose} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-confirm-body" style={{ display: 'grid', gap: 12 }}>
          <p style={{ margin: 0 }}>{t('account.link_desc')}</p>
          {error && (
            <div className="fx-inline-error" role="alert">
              {error}
            </div>
          )}
          <label className="fx-field" style={{ margin: 0 }}>
            <span className="fx-field-label">{t('account.current_password')}</span>
            <input
              className="fx-input"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoFocus
            />
          </label>

          {hasSecondFactor && method === 'totp' && (
            <label className="fx-field" style={{ margin: 0 }}>
              <span className="fx-field-label">{t('account.current_code')}</span>
              <div className="fx-auth" style={{ position: 'static', padding: 0, background: 'none' }}>
                <OtpInput value={code} onChange={setCode} disabled={busy} />
              </div>
            </label>
          )}
          {hasSecondFactor && method === 'recovery' && (
            <label className="fx-field" style={{ margin: 0 }}>
              <span className="fx-field-label">{t('account.recovery_code')}</span>
              <input
                className="fx-input"
                type="text"
                autoComplete="one-time-code"
                value={code}
                placeholder={t('account.recovery_placeholder')}
                onChange={(e) => setCode(e.target.value)}
              />
            </label>
          )}
          {hasSecondFactor && canMailCode && method === 'totp' && <MailCodeButton disabled={busy} />}
          {hasSecondFactor && (
            <button
              type="button"
              className="fx-btn"
              onClick={() => {
                setMethod(method === 'totp' ? 'recovery' : 'totp')
                setCode('')
              }}
            >
              {method === 'totp' ? t('account.use_recovery') : t('account.use_authenticator')}
            </button>
          )}
        </div>

        <footer className="fx-confirm-foot">
          <button type="button" className="fx-confirm-btn" onClick={onClose}>
            {t('common.cancel')}
          </button>
          <button
            type="submit"
            className="fx-confirm-btn fx-confirm-btn-primary"
            disabled={busy || !password || !codeReady}
          >
            {t('account.continue_google')}
          </button>
        </footer>
      </form>
    </div>
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

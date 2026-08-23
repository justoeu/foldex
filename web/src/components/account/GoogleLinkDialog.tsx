import { useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from '../icons'
import { OtpInput, OTP_LENGTH } from '../auth/OtpInput'
import { PasswordInput } from '../PasswordInput'
import { accountErrorMessage } from './accountErrors'
import * as auth from '../../api/auth'
import { MailCodeButton } from './MailCodeButton'
import { useEscape } from '../../hooks/useEscape'
import { useFocusTrap } from '../../hooks/useFocusTrap'

export function GoogleLinkDialog({
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
      setError(accountErrorMessage(err, t))
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
          <label className="fx-field">
            <span className="fx-field-label">{t('account.current_password')}</span>
            <PasswordInput
              className="fx-input"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoFocus
            />
          </label>

          {hasSecondFactor && method === 'totp' && (
            <label className="fx-field">
              <span className="fx-field-label">{t('account.current_code')}</span>
              <div className="fx-authfield">
                <OtpInput value={code} onChange={setCode} disabled={busy} />
              </div>
            </label>
          )}
          {hasSecondFactor && method === 'recovery' && (
            <label className="fx-field">
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

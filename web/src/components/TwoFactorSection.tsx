import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from './icons'
import { OtpInput, OTP_LENGTH } from './auth/OtpInput'
import { RecoveryCodes } from './auth/RecoveryCodes'
import {
  confirmTotp,
  disableTotp,
  fetchTwoFactorStatus,
  regenerateRecoveryCodes,
  startTotp,
  type TotpEnrollment,
} from '../api/twofa'
import { apiErrorCode as errCode } from '../lib/apiError'
import { useConfirm } from './ConfirmDialog'
import { useAuth } from '../auth/AuthProvider'

/**
 * Manages the caller's own second factor from Settings.
 *
 * This is the ONLY way a non-admin can turn 2FA on: the mandatory-enrollment
 * flow only fires for administrators mid-login, so without this section the
 * feature would be unreachable for everyone else.
 */
export function TwoFactorSection() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const confirmAction = useConfirm()
  const { reload } = useAuth()

  const status = useQuery({ queryKey: ['twofa', 'status'], queryFn: fetchTwoFactorStatus })
  const [enrollment, setEnrollment] = useState<TotpEnrollment | null>(null)
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const enabled = status.data?.enabled ?? false
  const required = status.data?.required ?? false
  const remaining = status.data?.recovery_codes_remaining ?? 0

  function reset() {
    setEnrollment(null)
    setPassword('')
    setCode('')
    setError('')
  }

  async function begin() {
    setBusy(true)
    setError('')
    try {
      setEnrollment(await startTotp(password))
      setCode('')
    } catch (err) {
      setError(messageFor(err, t))
    } finally {
      setBusy(false)
    }
  }

  async function confirm() {
    setBusy(true)
    setError('')
    try {
      const res = await confirmTotp(code)
      setCodes(res.recovery_codes)
      setEnrollment(null)
      setPassword('')
      setCode('')
      await qc.invalidateQueries({ queryKey: ['twofa'] })
      // The session's `user.totp_enabled` lives in AuthProvider, outside the
      // query cache, so invalidating above does not touch it. Re-probing /me is
      // what keeps the two from disagreeing until the next reload.
      await reload()
    } catch (err) {
      setError(messageFor(err, t))
      setCode('')
    } finally {
      setBusy(false)
    }
  }

  async function turnOff() {
    const ok = await confirmAction({
      title: t('twofa.disable'),
      message: t('twofa.disable_warning'),
      destructive: true,
    })
    if (!ok) return
    setBusy(true)
    setError('')
    try {
      await disableTotp(password, code)
      reset()
      await qc.invalidateQueries({ queryKey: ['twofa'] })
      await reload()
    } catch (err) {
      setError(messageFor(err, t))
      setCode('')
    } finally {
      setBusy(false)
    }
  }

  async function regenerate() {
    setBusy(true)
    setError('')
    try {
      setCodes(await regenerateRecoveryCodes(password, code))
      setPassword('')
      setCode('')
      await qc.invalidateQueries({ queryKey: ['twofa'] })
    } catch (err) {
      setError(messageFor(err, t))
      setCode('')
    } finally {
      setBusy(false)
    }
  }

  if (codes) {
    return (
      <section className="fx-card">
        <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
          <h3 className="fx-card-title" style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
            <Icon d={I.lock} size={15} /> {t('twofa.codes_title')}
          </h3>
          <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>{t('twofa.codes_subtitle')}</p>
          <div className="fx-auth">
            {/* Reuses the auth-surface component, wrapped in .fx-auth so its
                scoped styles apply outside the auth screens. */}
            <RecoveryCodes codes={codes} onDone={() => setCodes(null)} />
          </div>
        </div>
      </section>
    )
  }

  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3 className="fx-card-title" style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon d={I.lock} size={15} /> {t('twofa.section_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>{t('twofa.section_desc')}</p>

        <div
          style={{
            fontSize: 12,
            display: 'flex',
            alignItems: 'center',
            gap: 6,
            color: enabled ? 'var(--fx-ink-2)' : 'var(--fx-ink-4)',
          }}
        >
          <Icon d={enabled ? I.check : I.info} size={13} />{' '}
          {enabled ? t('twofa.status_on') : t('twofa.status_off')}
        </div>

        {enabled && (
          <div style={{ fontSize: 12, color: 'var(--fx-ink-3)' }}>
            {t('twofa.remaining', { count: remaining })}
          </div>
        )}
        {enabled && required && (
          <div style={{ fontSize: 12, color: 'var(--fx-ink-3)', display: 'flex', alignItems: 'center', gap: 6 }}>
            <Icon d={I.info} size={12} /> {t('twofa.required_note')}
          </div>
        )}

        {error && (
          <div className="fx-inline-error" role="alert" style={{ fontSize: 12 }}>
            {error}
          </div>
        )}

        {/* Every transition — on, off, new codes — demands the password. A
            stolen session must not be enough to add, remove or replace the
            factor that exists to contain exactly that. */}
        <label className="fx-field" style={{ margin: 0 }}>
          <span className="fx-field-label">{t('twofa.current_password')}</span>
          <input
            className="fx-input"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>

        {enrollment && (
          <>
            <div className="fx-auth" style={{ position: 'static', padding: 0, background: 'none' }}>
              <div className="fx-auth-qr">
                <img src={enrollment.qr_url} alt={t('twofa.qr_alt')} width={240} height={240} />
              </div>
            </div>
            <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0, wordBreak: 'break-all' }}>
              {enrollment.secret}
            </p>
          </>
        )}

        {(enabled || enrollment) && (
          <label className="fx-field" style={{ margin: 0 }}>
            <span className="fx-field-label">{t('twofa.current_code')}</span>
            <div className="fx-auth" style={{ position: 'static', padding: 0, background: 'none' }}>
              <OtpInput value={code} onChange={setCode} disabled={busy} />
            </div>
          </label>
        )}

        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {!enabled && !enrollment && (
            <button className="fx-btn fx-btn-primary" disabled={busy || !password} onClick={() => void begin()}>
              {t('twofa.enable')}
            </button>
          )}
          {enrollment && (
            <>
              <button
                className="fx-btn fx-btn-primary"
                disabled={busy || code.length < OTP_LENGTH}
                onClick={() => void confirm()}
              >
                {t('twofa.confirm')}
              </button>
              <button className="fx-btn" disabled={busy} onClick={reset}>
                {t('common.cancel')}
              </button>
            </>
          )}
          {enabled && (
            <>
              <button
                className="fx-btn"
                disabled={busy || !password || code.length < OTP_LENGTH}
                onClick={() => void regenerate()}
              >
                {t('twofa.regenerate')}
              </button>
              {!required && (
                <button
                  className="fx-btn fx-btn-danger"
                  disabled={busy || !password || code.length < OTP_LENGTH}
                  onClick={() => void turnOff()}
                >
                  {t('twofa.disable')}
                </button>
              )}
            </>
          )}
        </div>
      </div>
    </section>
  )
}

function messageFor(err: unknown, t: (k: string, o?: Record<string, unknown>) => string): string {
  switch (errCode(err)) {
    case 'invalid_credentials':
      return t('twofa.wrong_password')
    case 'invalid_code':
      return t('auth_errors.invalid_code')
    case 'totp_already_enabled':
      return t('twofa.already_enabled')
    case 'totp_required_for_admins':
      return t('twofa.required_note')
    default:
      return t('auth_errors.generic')
  }
}

import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { errorCode, errorStatus } from '../../api/auth'
import { confirmTotp, startTotp, type TotpEnrollment } from '../../api/twofa'
import { useAuth } from '../../auth/AuthProvider'
import { AuthShell, AuthError, AuthSubmit } from './AuthShell'
import { OtpInput, OTP_LENGTH } from './OtpInput'
import { RecoveryCodes } from './RecoveryCodes'

/**
 * Mandatory authenticator enrollment for an administrator.
 *
 * Reached mid-login, holding only the pre-auth cookie — there is no session
 * yet, and confirming produces one. The screen therefore has no "skip": the
 * policy exists precisely so that an admin password alone is never enough.
 */
export function EnrollTotpScreen() {
  const { t } = useTranslation()
  const { adopt, signOut } = useAuth()

  const [enrollment, setEnrollment] = useState<TotpEnrollment | null>(null)
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)
  const [pendingSession, setPendingSession] = useState<Parameters<typeof adopt>[0] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [showSecret, setShowSecret] = useState(false)

  // A ref, and DELIBERATELY no per-effect `alive` flag — see the same guard in
  // VerifyEmailScreen for why the flag breaks it.
  //
  // What must be prevented is the second REQUEST, not the second setState:
  // `startTotp` mints a new secret and overwrites the pending row, so firing it
  // twice replaces the seed under a user already scanning the first QR, and
  // out-of-order responses leave the manual-entry key disagreeing with what the
  // server stored. Both StrictMode's double mount and a mid-enrollment language
  // change would do that — `t` is a new function identity on every locale
  // switch, so it must not be a dependency here.
  const started = useRef(false)
  useEffect(() => {
    if (started.current) return
    started.current = true
    startTotp()
      .then(setEnrollment)
      .catch(() => setError(t('twofa.enroll_failed')))
    // eslint-disable-next-line react-hooks/exhaustive-deps -- see the ref above:
    // re-running this effect replaces a live enrollment secret.
  }, [])

  async function submit(raw: string) {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const res = await confirmTotp(raw)
      // Show the recovery codes BEFORE adopting the session. Adopting swaps the
      // gate over to <App/>, and the codes are displayed exactly once — the
      // server keeps only their hashes and cannot show them again.
      setCodes(res.recovery_codes)
      setPendingSession(res)
    } catch (err) {
      const c = errorCode(err)
      if (c === 'invalid_code') setError(t('auth_errors.invalid_code'))
      else if (c === 'challenge_invalid') setError(t('auth_otp.expired'))
      else if (errorStatus(err) === 0) setError(t('auth_errors.network'))
      else setError(t('auth_errors.generic'))
      setCode('')
    } finally {
      setBusy(false)
    }
  }

  if (codes && pendingSession) {
    return (
      <AuthShell
        kicker={t('twofa.codes_kicker')}
        title={t('twofa.codes_title')}
        subtitle={t('twofa.codes_subtitle')}
      >
        <RecoveryCodes codes={codes} onDone={() => adopt(pendingSession)} />
      </AuthShell>
    )
  }

  return (
    <AuthShell
      kicker={t('twofa.enroll_kicker')}
      title={t('twofa.enroll_title')}
      subtitle={t('twofa.enroll_subtitle')}
    >
      <form
        className="fx-auth-form"
        onSubmit={(e) => {
          e.preventDefault()
          void submit(code)
        }}
        noValidate
      >
        <AuthError message={error} />

        {enrollment ? (
          <>
            <div className="fx-auth-qr">
              {/*
                The QR is rendered by the server (/2fa/totp/qr.png). It keeps the
                base32 seed out of any JavaScript QR library and adds no frontend
                dependency; the endpoint sends Cache-Control: no-store, because
                the image IS the secret in visual form.
              */}
              <img src={enrollment.qr_url} alt={t('twofa.qr_alt')} width={240} height={240} />
            </div>

            <button
              type="button"
              className="fx-auth-link"
              onClick={() => setShowSecret((v) => !v)}
              aria-expanded={showSecret}
            >
              {showSecret ? t('twofa.hide_secret') : t('twofa.cannot_scan')}
            </button>
            {showSecret && (
              <p className="fx-auth-secret" data-testid="totp-secret">
                {enrollment.secret}
              </p>
            )}

            <p className="fx-auth-hint">{t('twofa.enter_code_hint')}</p>
            <OtpInput
              value={code}
              onChange={setCode}
              onComplete={(full) => void submit(full)}
              disabled={busy}
              autoFocus
              invalid={Boolean(error)}
            />

            <AuthSubmit busy={busy} disabled={code.length < OTP_LENGTH}>
              {t('twofa.confirm')}
            </AuthSubmit>
          </>
        ) : (
          !error && (
            <p className="fx-auth-notice" role="status">
              <span className="fx-auth-spinner" aria-hidden="true" /> {t('auth.loading')}
            </p>
          )
        )}

        <div className="fx-auth-alt">
          <button type="button" className="fx-auth-link" onClick={() => void signOut()}>
            {t('auth_otp.cancel')}
          </button>
        </div>
      </form>
    </AuthShell>
  )
}

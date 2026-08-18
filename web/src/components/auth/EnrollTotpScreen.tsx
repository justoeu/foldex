import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { errorCode, errorStatus } from '../../api/auth'
import {
  confirmEmailFactor,
  confirmTotp,
  startEmailFactor,
  startTotp,
  type EmailFactorEnrollment,
  type FactorMethod,
  type TotpEnrollment,
} from '../../api/twofa'
import { useAuth } from '../../auth/AuthProvider'
import { AuthShell, AuthError, AuthSubmit } from './AuthShell'
import { OtpInput, OTP_LENGTH } from './OtpInput'
import { RecoveryCodes } from './RecoveryCodes'

/**
 * Mandatory second-factor enrollment for an administrator.
 *
 * Reached mid-login, holding only the pre-auth cookie — there is no session
 * yet, and confirming produces one. The screen therefore has no "skip": the
 * policy exists precisely so that an admin password alone is never enough.
 *
 * Since ADR-37 the admin CHOOSES a method, so the screen opens on that choice
 * rather than firing an enrollment on mount. That is also what makes the
 * ref-guard below sound: nothing starts until a deliberate click.
 */
export function EnrollTotpScreen() {
  const { t } = useTranslation()
  const { adopt, signOut, session } = useAuth()
  // The gate renders this screen from a two_factor_required session, whose
  // payload carries the instance features — including whether a mailed code
  // could arrive at all.
  const emailAvailable = session.status !== 'loading' && session.features.email_delivery

  // Null means "ask". An instance with no SMTP offers no choice at all, so it
  // starts the authenticator straight away rather than showing a one-button
  // chooser — the screen is mandatory and mid-login, and a question with one
  // possible answer is pure friction there.
  const [method, setMethod] = useState<FactorMethod | null>(
    () => (emailAvailable ? null : 'totp'),
  )
  const [totp, setTotp] = useState<TotpEnrollment | null>(null)
  const [mailed, setMailed] = useState<EmailFactorEnrollment | null>(null)
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
  // starting an enrollment mints a new secret (or mails a new code) and
  // supersedes the pending row, so firing it twice replaces what the user is
  // already looking at, and out-of-order responses leave the manual-entry key
  // disagreeing with what the server stored. Both StrictMode's double mount and
  // a mid-enrollment language change would do that — `t` is a new function
  // identity on every locale switch, so it must not be a dependency here.
  const started = useRef<FactorMethod | null>(null)
  useEffect(() => {
    if (!method || started.current === method) return
    started.current = method
    if (method === 'totp') {
      startTotp()
        .then(setTotp)
        .catch(() => setError(t('twofa.enroll_failed')))
    } else {
      startEmailFactor()
        .then(setMailed)
        .catch(() => setError(t('twofa.enroll_failed')))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- see the ref above:
    // re-running this effect replaces a live enrollment secret.
  }, [method])

  async function submit(raw: string) {
    if (busy || !method) return
    setBusy(true)
    setError('')
    try {
      const res = method === 'totp' ? await confirmTotp(raw) : await confirmEmailFactor(raw)
      // Show the recovery codes BEFORE adopting the session. Adopting swaps the
      // gate over to <App/>, and the codes are displayed exactly once — the
      // server keeps only their keyed digests and cannot show them again.
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

  const ready = method === 'totp' ? totp !== null : mailed !== null

  return (
    <AuthShell
      kicker={t('twofa.enroll_kicker')}
      title={t('twofa.enroll_title')}
      subtitle={method ? t('twofa.enroll_subtitle') : t('twofa.enroll_choose_subtitle')}
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

        {!method ? (
          <MethodChoice onPick={setMethod} />
        ) : ready ? (
          <>
            {method === 'totp' && totp && (
              <>
                <div className="fx-auth-qr">
                  {/*
                    The QR is rendered by the server (/2fa/totp/qr.png). It keeps
                    the base32 seed out of any JavaScript QR library and adds no
                    frontend dependency; the endpoint sends Cache-Control:
                    no-store, because the image IS the secret in visual form.
                  */}
                  <img src={totp.qr_url} alt={t('twofa.qr_alt')} width={240} height={240} />
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
                    {totp.secret}
                  </p>
                )}
              </>
            )}

            {method === 'email' && mailed && (
              <p className="fx-auth-hint">
                {t('twofa.enroll_email_sent', { account: mailed.account })}
              </p>
            )}

            <p className="fx-auth-hint">
              {method === 'totp' ? t('twofa.enter_code_hint') : t('twofa.enter_mailed_hint')}
            </p>
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
          {/*
            No "skip". The only way out is signing out — the policy exists so
            that an administrator password alone is never a session.
          */}
          <button type="button" className="fx-auth-link" onClick={() => void signOut()}>
            {t('auth_otp.cancel')}
          </button>
        </div>
      </form>
    </AuthShell>
  )
}

// Rendered only when `method` is null, which the initializer above only permits
// when e-mail delivery is configured — so there is no "e-mail unavailable"
// branch here to guard. A guard would be unreachable code dressed as caution.
function MethodChoice({ onPick }: { onPick: (method: FactorMethod) => void }) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      <button type="button" className="fx-auth-submit" onClick={() => onPick('totp')}>
        {t('twofa.enable_app')}
      </button>
      <button type="button" className="fx-btn" onClick={() => onPick('email')}>
        {t('twofa.enable_email')}
      </button>
      <p className="fx-auth-hint">{t('twofa.enroll_choose_hint')}</p>
    </div>
  )
}

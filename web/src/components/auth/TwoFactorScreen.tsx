import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { apiErrorCode, apiErrorStatus } from '../../lib/apiError'
import { sendEmailOtp, verifyTwoFactor } from '../../api/twofa'
import { useAuth } from '../../auth/AuthProvider'
import type { TwoFactorPending } from '../../auth/types'
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell'
import { OtpInput, OTP_LENGTH } from './OtpInput'

/**
 * The code screen: one field, three accepted credentials.
 *
 * A user with a lost phone types a recovery code into the same box, and one
 * who has neither asks for a mailed code. Splitting these into separate screens
 * would make the recovery path — used exactly when someone is already stressed
 * — the hardest one to find.
 */

/** The window a code entry gets, per the product spec. Deliberately far
 *  shorter than the challenge's own TTL: the backend keeps the challenge
 *  alive for ten minutes so a slow mail relay cannot kill a legitimate
 *  sign-in, while the SCREEN enforces its own cadence and offers the
 *  restart (resend, or back to login) at the pace the user expects. */
const ENTRY_WINDOW_SECONDS = 60

/** How long an expired panel stays up before the automatic return to the
 *  login form. Long enough to read, short enough that nobody waits for it. */
const REDIRECT_AFTER_MS = 3000

type Phase = 'counting' | 'askResend' | 'expiredFinal'

export function TwoFactorScreen({ pending }: Readonly<{ pending: TwoFactorPending }>) {
  const { t } = useTranslation()
  const { adopt, signOut } = useAuth()

  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState('')
  const [useRecovery, setUseRecovery] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [remaining, setRemaining] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [emailSubmitting, setEmailSubmitting] = useState(false)
  const emailSubmittingRef = useRef(false)

  // The expiry ladder: counting → (email? ask to resend : final) → final.
  // `resendUsed` makes the second window the last one, so "send again" can
  // never become a loop of its own.
  const [secondsLeft, setSecondsLeft] = useState(ENTRY_WINDOW_SECONDS)
  const [phase, setPhase] = useState<Phase>('counting')
  const [emailRequested, setEmailRequested] = useState(false)
  const [resendUsed, setResendUsed] = useState(false)
  const emailRequestedRef = useRef(false)

  useEffect(() => {
    if (phase !== 'counting') return
    const tick = setInterval(() => {
      setSecondsLeft((s) => {
        if (s > 1) return s - 1
        return 0
      })
    }, 1000)
    return () => clearInterval(tick)
  }, [phase])

  // The tick above only lands at 0; the transition is its own effect so a
  // phase change mid-window (resend) can restart the clock cleanly.
  useEffect(() => {
    if (phase === 'counting' && secondsLeft === 0) {
      if (emailRequestedRef.current && !resendUsed) setPhase('askResend')
      else setPhase('expiredFinal')
    }
  }, [secondsLeft, phase, resendUsed])

  // The expired panel redirects on its own; signOut (not a route push) so the
  // pre-auth cookie dies too — otherwise the next login replays this very
  // challenge and the "expired" screen greets a user who just typed a
  // password.
  useEffect(() => {
    if (phase !== 'expiredFinal') return
    const redirect = setTimeout(() => void signOut(), REDIRECT_AFTER_MS)
    return () => clearTimeout(redirect)
  }, [phase, signOut])

  const submit = useCallback(
    async (raw: string) => {
      if (busy || !raw.trim()) return
      setBusy(true)
      setError('')
      setNotice('')
      try {
        const res = await verifyTwoFactor(raw)
        // `busy` is deliberately NOT cleared on success. A successful
        // verification is terminal for this screen: the gate swaps it for the
        // app, and until that render lands the form must stay inert. Clearing
        // it would leave a live submit button over a spent single-use code, so
        // one stray click would fail and paint an error over a login that
        // actually succeeded.
        adopt(res)
        return
      } catch (err) {
        const c = apiErrorCode(err)
        if (c === 'invalid_code') {
          const left = attemptsRemaining(err)
          setRemaining(left)
          setError(
            left === null
              ? t('auth_errors.invalid_code')
              : t('auth_otp.invalid_with_attempts', { count: left }),
          )
        } else if (c === 'too_many_attempts') {
          setError(t('auth_otp.locked_out'))
        } else if (c === 'challenge_invalid') {
          setError(t('auth_otp.expired'))
        } else if ((apiErrorStatus(err) ?? 0) === 0) {
          setError(t('auth_errors.network'))
        } else {
          setError(t('auth_errors.generic'))
        }
        // Clear the field so the next attempt starts from an empty box rather
        // than forcing the user to delete six digits by hand.
        setCode('')
        setRecovery('')
        setBusy(false)
      }
    },
    [adopt, busy, t],
  )

  async function requestEmail() {
    if (emailSubmittingRef.current) return
    emailSubmittingRef.current = true
    setEmailSubmitting(true)
    setError('')
    setNotice('')
    let sent = false
    try {
      await sendEmailOtp()
      sent = true
    } catch (err) {
      setError(t((apiErrorStatus(err) ?? 0) === 0 ? 'auth_errors.network' : 'auth_errors.generic'))
    } finally {
      emailSubmittingRef.current = false
      setEmailSubmitting(false)
    }
    if (sent) {
      setEmailRequested(true)
      emailRequestedRef.current = true
      setNotice(t('auth_otp.email_sent', { email: pending.email }))
    }
  }

  function resendAfterExpiry() {
    setResendUsed(true)
    setSecondsLeft(ENTRY_WINDOW_SECONDS)
    setPhase('counting')
    setNotice('')
    void requestEmail()
  }

  const canEmail = pending.methods.includes('email_otp')

  if (phase === 'askResend') {
    return (
      <AuthShell kicker={t('auth_otp.kicker')} title={t('auth_otp.expired_title')}>
        <p className="fx-auth-sub">{t('auth_otp.expired_email_desc', { email: pending.email })}</p>
        <div className="fx-auth-alt fx-auth-alt-center">
          <button type="button" className="fx-btn fx-btn-primary" onClick={resendAfterExpiry}>
            {t('auth_otp.resend_action')}
          </button>
          <button type="button" className="fx-auth-link" onClick={() => setPhase('expiredFinal')}>
            {t('auth_otp.back_to_login')}
          </button>
        </div>
      </AuthShell>
    )
  }

  if (phase === 'expiredFinal') {
    return (
      <AuthShell kicker={t('auth_otp.kicker')} title={t('auth_otp.expired_title')}>
        <p className="fx-auth-sub">{t('auth_otp.expired_final_desc')}</p>
        <div className="fx-auth-alt fx-auth-alt-center">
          <button type="button" className="fx-btn fx-btn-primary" onClick={() => void signOut()}>
            {t('auth_otp.back_to_login')}
          </button>
        </div>
      </AuthShell>
    )
  }

  return (
    <AuthShell
      kicker={t('auth_otp.kicker')}
      title={t('auth_otp.title')}
      subtitle={t('auth_otp.subtitle', { email: pending.email })}
    >
      <form
        className="fx-auth-form"
        onSubmit={(e) => {
          e.preventDefault()
          void submit(useRecovery ? recovery : code)
        }}
        noValidate
      >
        <AuthError message={error} />
        {notice && (
          <output className="fx-auth-notice">
            {notice}
          </output>
        )}

        {useRecovery ? (
          <AuthField id="fx-otp-recovery" label={t('auth_otp.recovery_label')}>
            <input
              id="fx-otp-recovery"
              className="fx-auth-input fx-auth-input-mono"
              type="text"
              autoComplete="off"
              autoCapitalize="characters"
              spellCheck={false}
              autoFocus
              value={recovery}
              onChange={(e) => setRecovery(e.target.value)}
              placeholder={t('auth_otp.recovery_placeholder')}
            />
          </AuthField>
        ) : (
          <OtpInput
            value={code}
            onChange={setCode}
            // Auto-submit on the sixth digit: the user has finished, and making
            // them reach for a button afterwards is friction with no purpose.
            onComplete={(full) => void submit(full)}
            disabled={busy}
            autoFocus
            invalid={Boolean(error) && remaining !== null}
          />
        )}

        <AuthSubmit busy={busy} disabled={useRecovery ? !recovery.trim() : code.length < OTP_LENGTH}>
          {t('auth_otp.submit')}
        </AuthSubmit>

        <p className="fx-otp-countdown" role="timer" aria-live="off">
          {t('auth_otp.countdown', { count: secondsLeft })}
        </p>

        <div className="fx-auth-alt">
          <button
            type="button"
            className="fx-auth-link"
            onClick={() => {
              setUseRecovery((v) => !v)
              setError('')
              setCode('')
              setRecovery('')
            }}
          >
            {useRecovery ? t('auth_otp.use_app') : t('auth_otp.use_recovery')}
          </button>
          {canEmail && !useRecovery && (
            <button type="button" className="fx-auth-link" disabled={emailSubmitting} onClick={() => void requestEmail()}>
              {t('auth_otp.send_email')}
            </button>
          )}
          <button type="button" className="fx-auth-link" onClick={() => void signOut()}>
            {t('auth_otp.cancel')}
          </button>
        </div>
      </form>
    </AuthShell>
  )
}

/**
 * Reads `attempts_remaining` off the 401 body.
 *
 * The backend puts it alongside the error envelope rather than inside it, so
 * this cannot go through apiErrorCode's path.
 */
function attemptsRemaining(err: unknown): number | null {
  const e = err as { response?: { data?: { attempts_remaining?: number } } }
  const n = e?.response?.data?.attempts_remaining
  return typeof n === 'number' ? n : null
}

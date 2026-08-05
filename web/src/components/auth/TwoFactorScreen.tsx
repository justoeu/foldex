import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { errorCode, errorStatus } from '../../api/auth'
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
export function TwoFactorScreen({ pending }: { pending: TwoFactorPending }) {
  const { t } = useTranslation()
  const { adopt, signOut } = useAuth()

  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState('')
  const [useRecovery, setUseRecovery] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [remaining, setRemaining] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)

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
        const c = errorCode(err)
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
        } else if (errorStatus(err) === 0) {
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
    setError('')
    try {
      await sendEmailOtp()
    } catch {
      // Deliberately ignored. The endpoint answers 202 whether it sent or
      // throttled, and surfacing a distinct outcome here would turn the button
      // into a probe for how many codes have already gone out.
    }
    setNotice(t('auth_otp.email_sent', { email: pending.email }))
  }

  const canEmail = pending.methods.includes('email_otp')

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
          <p className="fx-auth-notice" role="status">
            {notice}
          </p>
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
            <button type="button" className="fx-auth-link" onClick={() => void requestEmail()}>
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
 * this cannot go through errorCode's path.
 */
function attemptsRemaining(err: unknown): number | null {
  const e = err as { response?: { data?: { attempts_remaining?: number } } }
  const n = e?.response?.data?.attempts_remaining
  return typeof n === 'number' ? n : null
}

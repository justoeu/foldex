import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  confirmEmailFactor,
  confirmTotp,
  type EmailFactorEnrollment,
  type FactorMethod,
  type TotpEnrollment,
} from '../../api/twofa'
import { useAuth } from '../../auth/AuthProvider'
import { AuthShell, AuthError, AuthSubmit } from './AuthShell'
import { OtpInput, OTP_LENGTH } from './OtpInput'
import { RecoveryCodes } from './RecoveryCodes'
import {
  enrollPhase,
  enrollSubmitErrorKey,
  initialEnrollMethod,
} from './EnrollTotpScreen.state'
import { useEnrollStart } from './EnrollTotpScreen.start'

/**
 * Mandatory second-factor enrollment for an administrator.
 *
 * Reached mid-login, holding only the pre-auth cookie — there is no session
 * yet, and confirming produces one. The screen therefore has no "skip": the
 * policy exists precisely so that an admin password alone is never enough.
 *
 * Since ADR-37 the admin CHOOSES a method, so the screen opens on that choice
 * rather than firing an enrollment on mount. That is also what makes the
 * ref-guard in useEnrollStart sound: nothing starts until a deliberate click.
 */
export function EnrollTotpScreen() {
  const { t } = useTranslation()
  const { adopt, signOut, session } = useAuth()
  // The gate renders this screen from a two_factor_required session, whose
  // payload carries the instance features — including whether a mailed code
  // could arrive at all.
  const emailAvailable = session.status !== 'loading' && session.features.email_delivery

  const [method, setMethod] = useState<FactorMethod | null>(() => initialEnrollMethod(emailAvailable))
  const [totp, setTotp] = useState<TotpEnrollment | null>(null)
  const [mailed, setMailed] = useState<EmailFactorEnrollment | null>(null)
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)
  const [pendingSession, setPendingSession] = useState<Parameters<typeof adopt>[0] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [showSecret, setShowSecret] = useState(false)

  useEnrollStart(method, {
    totp: setTotp,
    email: setMailed,
    error: () => setError(t('twofa.enroll_failed')),
  })

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
      setError(t(enrollSubmitErrorKey(err)))
      setCode('')
    } finally {
      setBusy(false)
    }
  }

  const phase = enrollPhase({ method, totp, mailed, codes, pendingSession })

  if (phase === 'codes' && codes && pendingSession) {
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

        {phase === 'choose' && <MethodChoice onPick={setMethod} />}
        {phase === 'confirm' && (
          <EnrollReadyForm
            method={method!}
            totp={totp}
            mailed={mailed}
            code={code}
            busy={busy}
            error={error}
            showSecret={showSecret}
            onCode={setCode}
            onToggleSecret={() => setShowSecret((v) => !v)}
            onComplete={(full) => void submit(full)}
          />
        )}
        {phase === 'enroll' && !error && (
          <p className="fx-auth-notice" role="status">
            <span className="fx-auth-spinner" aria-hidden="true" /> {t('auth.loading')}
          </p>
        )}

        <div className="fx-auth-alt">
          {/*
            No "skip" (enrollMaySkip is false by construction). The only way
            out is signing out — the policy exists so that an administrator
            password alone is never a session.
          */}
          <button type="button" className="fx-auth-link" onClick={() => void signOut()}>
            {t('auth_otp.cancel')}
          </button>
        </div>
      </form>
    </AuthShell>
  )
}

function EnrollReadyForm({
  method,
  totp,
  mailed,
  code,
  busy,
  error,
  showSecret,
  onCode,
  onToggleSecret,
  onComplete,
}: {
  method: FactorMethod
  totp: TotpEnrollment | null
  mailed: EmailFactorEnrollment | null
  code: string
  busy: boolean
  error: string
  showSecret: boolean
  onCode: (next: string) => void
  onToggleSecret: () => void
  onComplete: (full: string) => void
}) {
  const { t } = useTranslation()
  return (
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
            onClick={onToggleSecret}
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
        onChange={onCode}
        onComplete={onComplete}
        disabled={busy}
        autoFocus
        invalid={Boolean(error)}
      />

      <AuthSubmit busy={busy} disabled={code.length < OTP_LENGTH}>
        {t('twofa.confirm')}
      </AuthSubmit>
    </>
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

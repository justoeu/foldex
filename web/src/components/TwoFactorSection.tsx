import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { OtpInput, OTP_LENGTH } from './auth/OtpInput'
import { RecoveryCodes } from './auth/RecoveryCodes'
import { useTwoFactorController } from '../hooks/useTwoFactorController'

type Controller = ReturnType<typeof useTwoFactorController>

export function TwoFactorSection() {
  const controller = useTwoFactorController()
  return controller.codes
    ? <RecoveryCodesPanel controller={controller} />
    : <TwoFactorSettings controller={controller} />
}

function RecoveryCodesPanel({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3 className="fx-card-title" style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon d={I.lock} size={15} /> {t('twofa.codes_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>{t('twofa.codes_subtitle')}</p>
        <div className="fx-auth">
          <RecoveryCodes codes={controller.codes ?? []} onDone={controller.dismissCodes} />
        </div>
      </div>
    </section>
  )
}

function TwoFactorSettings({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3 className="fx-card-title" style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon d={I.lock} size={15} /> {t('twofa.section_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>{t('twofa.section_desc')}</p>
        <TwoFactorStatus controller={controller} />
        {controller.error && (
          <div className="fx-inline-error" role="alert" style={{ fontSize: 12 }}>
            {controller.error}
          </div>
        )}
        <TwoFactorProofFields controller={controller} />
        <TwoFactorActions controller={controller} />
      </div>
    </section>
  )
}

function TwoFactorStatus({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  return (
    <>
      <div style={{
        fontSize: 12,
        display: 'flex',
        alignItems: 'center',
        gap: 6,
        color: controller.enabled ? 'var(--fx-ink-2)' : 'var(--fx-ink-4)',
      }}>
        <Icon d={controller.enabled ? I.check : I.info} size={13} />{' '}
        {controller.enabled ? t('twofa.status_on') : t('twofa.status_off')}
      </div>
      {controller.enabled && (
        <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'grid', gap: 4 }}>
          <MethodRow on={controller.totpEnabled} label={t('twofa.method_app')} />
          <MethodRow on={controller.emailEnabled} label={t('twofa.method_email')} />
        </ul>
      )}
      {controller.enabled && (
        <div style={{ fontSize: 12, color: 'var(--fx-ink-3)' }}>
          {t('twofa.remaining', { count: controller.remaining })}
        </div>
      )}
      {controller.enabled && controller.required && (
        <div style={{ fontSize: 12, color: 'var(--fx-ink-3)', display: 'flex', alignItems: 'center', gap: 6 }}>
          <Icon d={I.info} size={12} /> {t('twofa.required_note')}
        </div>
      )}
    </>
  )
}

function MethodRow({ on, label }: { on: boolean; label: string }) {
  return (
    <li style={{
      fontSize: 12,
      display: 'flex',
      alignItems: 'center',
      gap: 6,
      color: on ? 'var(--fx-ink-2)' : 'var(--fx-ink-4)',
    }}>
      <Icon d={on ? I.check : I.info} size={12} /> {label}
    </li>
  )
}

function TwoFactorProofFields({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  return (
    <>
      <label className="fx-field" style={{ margin: 0 }}>
        <span className="fx-field-label">{t('twofa.current_password')}</span>
        <input
          className="fx-input"
          type="password"
          autoComplete="current-password"
          value={controller.password}
          onChange={(event) => controller.setPassword(event.target.value)}
        />
      </label>
      {controller.enrollment && <EnrollmentDetails controller={controller} />}
      {(controller.enabled || controller.enrollment) && (
        <label className="fx-field" style={{ margin: 0 }}>
          <span className="fx-field-label">{t('twofa.current_code')}</span>
          <div className="fx-auth" style={{ position: 'static', padding: 0, background: 'none' }}>
            <OtpInput value={controller.code} onChange={controller.setCode} disabled={controller.busy} />
          </div>
          <ProofHint controller={controller} />
        </label>
      )}
    </>
  )
}

/**
 * Says which code the field will accept, and offers to mail one.
 *
 * Not decoration: an account whose only factor is e-mail has no authenticator
 * to read a code from, and without this the field is a box it cannot fill.
 *
 * Silent during an enrollment. The code was already sent by the start call and
 * the paragraph above names the address, so a hint here would repeat it — and
 * a second send would supersede the code the user is holding.
 */
function ProofHint({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  if (controller.enrollment) return null
  if (!controller.emailEnabled) return null
  return (
    <span className="fx-field-hint" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
      {controller.codeSent ? t('twofa.code_sent') : t('twofa.code_accepts_mailed')}
      <button
        type="button"
        className="fx-field-action"
        disabled={controller.busy}
        onClick={() => void controller.mailStepUpCode()}
      >
        {t('twofa.mail_a_code')}
      </button>
    </span>
  )
}

function EnrollmentDetails({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  const enrollment = controller.enrollment
  if (!enrollment) return null
  if (enrollment.method === 'email') {
    return (
      <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>
        {t('twofa.enroll_email_subtitle', { account: enrollment.email.account })}
      </p>
    )
  }
  return (
    <>
      <div className="fx-auth" style={{ position: 'static', padding: 0, background: 'none' }}>
        <div className="fx-auth-qr">
          <img src={enrollment.totp.qr_url} alt={t('twofa.qr_alt')} width={240} height={240} />
        </div>
      </div>
      <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0, wordBreak: 'break-all' }}>
        {enrollment.totp.secret}
      </p>
    </>
  )
}

function TwoFactorActions({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  const proofMissing = !controller.password || controller.code.length < OTP_LENGTH
  return (
    <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
      {!controller.enrollment && !controller.totpEnabled && (
        <button
          className="fx-btn fx-btn-primary"
          disabled={controller.busy || !controller.password}
          onClick={() => void controller.begin('totp')}
        >
          {t('twofa.enable_app')}
        </button>
      )}
      {/*
        The e-mail method is offered only where a mailed code could actually
        arrive. On an instance with no SMTP the server refuses the enrollment,
        so a button here would be a promise the backend always breaks.
      */}
      {!controller.enrollment && !controller.emailEnabled && controller.emailAvailable && (
        <button
          className="fx-btn"
          disabled={controller.busy || !controller.password}
          onClick={() => void controller.begin('email')}
        >
          {t('twofa.enable_email')}
        </button>
      )}
      {controller.enrollment && (
        <>
          <button
            className="fx-btn fx-btn-primary"
            disabled={controller.busy || controller.code.length < OTP_LENGTH}
            onClick={() => void controller.confirm()}
          >
            {t('twofa.confirm')}
          </button>
          <button className="fx-btn" disabled={controller.busy} onClick={controller.reset}>
            {t('common.cancel')}
          </button>
        </>
      )}
      {controller.enabled && !controller.enrollment && (
        <>
          <button
            className="fx-btn"
            disabled={controller.busy || proofMissing}
            onClick={() => void controller.regenerate()}
          >
            {t('twofa.regenerate')}
          </button>
          {/*
            Each disable button follows the SERVER's can_disable_* answer rather
            than a rule re-derived here. An admin under a mandatory-2FA policy
            may drop one factor while the other stands, and a second copy of
            that rule in the browser is free to disagree with the one enforced.
          */}
          {controller.canDisableTotp && (
            <button
              className="fx-btn fx-btn-danger"
              disabled={controller.busy || proofMissing}
              onClick={() => void controller.turnOff('totp')}
            >
              {t('twofa.disable')}
            </button>
          )}
          {controller.canDisableEmail && (
            <button
              className="fx-btn fx-btn-danger"
              disabled={controller.busy || proofMissing}
              onClick={() => void controller.turnOff('email')}
            >
              {t('twofa.disable_email')}
            </button>
          )}
        </>
      )}
    </div>
  )
}

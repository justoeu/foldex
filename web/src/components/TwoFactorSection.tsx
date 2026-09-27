import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { OtpInput, OTP_LENGTH } from './auth/OtpInput'
import { RecoveryCodes } from './auth/RecoveryCodes'
import { useTwoFactorController } from '../hooks/useTwoFactorController'
import { PasswordInput } from './PasswordInput'
import { MailCodeHint } from './account/MailCodeButton'
import { Notice } from './account/SectionCard'
import {
  LOW_RECOVERY_CODES,
  methodActionDisabled,
  methodKind,
  twoFactorMethods,
  type MethodSnapshot,
} from './TwoFactorSection.methods'
import type { FactorMethod } from '../api/twofa'

type Controller = ReturnType<typeof useTwoFactorController>

/**
 * The second-factor surface, in three mutually exclusive states.
 *
 * Three renders rather than one that hides parts of itself: an enrollment in
 * flight and the one-time recovery codes are each a single task with a single
 * next action, and leaving the method list and the recovery band on screen
 * beside them offers ways out of a step the user has not finished.
 */
export function TwoFactorSection() {
  const controller = useTwoFactorController()
  if (controller.codes) return <RecoveryCodesPanel controller={controller} />
  if (controller.enrollment) return <EnrollmentPanel controller={controller} />
  return <TwoFactorOverview controller={controller} />
}

/* ─── overview ──────────────────────────────────────────────────────── */

function TwoFactorOverview({ controller }: Readonly<{ controller: Controller }>) {
  const { t } = useTranslation()
  const on = controller.enabled
  return (
    <div className="fx-acc2-2fa">
      {controller.error && <Notice tone="bad">{controller.error}</Notice>}
      <div className={on ? 'fx-acc2-banner-ok' : 'fx-acc2-banner-off'}>
        <span className={on ? 'fx-acc2-method-icon fx-acc2-method-icon-on' : 'fx-acc2-method-icon'} aria-hidden="true">
          <Icon d={I.shield} size={16} />
        </span>
        <div className="fx-acc2-banner-title">{on ? t('twofa.status_on') : t('twofa.status_off')}</div>
        <span className={on ? 'fx-acc2-pill fx-acc2-pill-ok' : 'fx-acc2-pill'}>
          {on ? t('twofa.badge_on') : t('twofa.badge_off')}
        </span>
      </div>
      {/*
        The proof comes BEFORE the actions it unlocks in the DOM. Every button
        is disabled until these fields are filled; with the form underneath,
        the screen read as broken controls and a form with no stated purpose.
        On a wide window the form keeps the first column and the list the rest,
        in that same order. A swapped column put the keyboard in the form on
        the right before the list on the left.
      */}
      <div className="fx-acc2-2fa-grid">
        <ProofPanel controller={controller} />
        <MethodList controller={controller} />
      </div>
    </div>
  )
}

function ProofPanel({ controller }: Readonly<{ controller: Controller }>) {
  const { t } = useTranslation()
  return (
    <div className="fx-acc2-card fx-acc2-2fa-proof">
      <div className="fx-acc2-card-body">
        <div>
          <div className="fx-acc2-panel-title">{t('twofa.proof_label')}</div>
          <span className="fx-acc2-field-hint">
            {controller.enabled ? t('twofa.proof_hint') : t('twofa.proof_hint_password')}
          </span>
        </div>
        <label className="fx-acc2-field">
          <span className="fx-acc2-field-label">{t('twofa.current_password')}</span>
          <PasswordInput
            className="fx-acc2-input"
            autoComplete="current-password"
            value={controller.password}
            onChange={(event) => controller.setPassword(event.target.value)}
          />
        </label>
        {controller.enabled && (
          <label className="fx-acc2-field">
            <span className="fx-acc2-field-label">{t('twofa.current_code')}</span>
            <div className="fx-authfield fx-2fa-otp">
              <OtpInput value={controller.code} onChange={controller.setCode} disabled={controller.busy} />
            </div>
            <ProofHint controller={controller} />
          </label>
        )}
      </div>
    </div>
  )
}

/**
 * Says which code the field will accept, and offers to mail one.
 *
 * Not decoration: an account whose only factor is e-mail has no authenticator
 * to read a code from, and without this the field is a box it cannot fill.
 */
function ProofHint({ controller }: Readonly<{ controller: Controller }>) {
  if (!controller.emailEnabled) return null
  return (
    <MailCodeHint
      sent={controller.codeSent}
      busy={controller.busy}
      onSend={() => void controller.mailStepUpCode()}
    />
  )
}

function MethodList({ controller }: Readonly<{ controller: Controller }>) {
  const { t } = useTranslation()
  const methods = twoFactorMethods({
    totpEnabled: controller.totpEnabled,
    canDisableTotp: controller.canDisableTotp,
    emailEnabled: controller.emailEnabled,
    canDisableEmail: controller.canDisableEmail,
    emailAvailable: controller.emailAvailable,
    twoFactorEnabled: controller.enabled,
  })
  return (
    <div className="fx-acc2-card fx-acc2-2fa-methods">
      <div className="fx-acc2-card-head">
        <span>{t('twofa.methods_label')}</span>
      </div>
      {methods.map((method) => (
        <MethodRow key={method.id} method={method} controller={controller} />
      ))}
    </div>
  )
}

function MethodRow({
  method,
  controller,
}: Readonly<{
  method: MethodSnapshot
  controller: Controller
}>) {
  const kind = methodKind(method)
  if (kind === 'hidden') return null
  if (method.id === 'recovery') {
    return <RecoveryRow controller={controller} />
  }
  return <FactorRow method={method} kind={kind} controller={controller} />
}

function RecoveryRow({ controller }: Readonly<{ controller: Controller }>) {
  const { t } = useTranslation()
  const low = controller.remaining < LOW_RECOVERY_CODES
  const name = t('twofa.remaining', { count: controller.remaining })
  return (
    <MethodLine
      icon={I.key}
      tone={low ? 'warn' : undefined}
      name={name}
      hint={low ? t('twofa.recovery_low') : t('twofa.recovery_hint')}
      action={
        <button
          type="button"
          className="fx-acc2-btn-outline"
          disabled={methodActionDisabled('regenerate', controller.password, controller.code, controller.busy)}
          onClick={() => void controller.regenerate()}
        >
          {t('twofa.regenerate')}
        </button>
      }
    />
  )
}

function methodIconClass(tone: 'on' | 'warn' | undefined): string {
  if (tone === 'on') return 'fx-acc2-method-icon fx-acc2-method-icon-on'
  if (tone === 'warn') return 'fx-acc2-method-icon fx-acc2-method-icon-warn'
  return 'fx-acc2-method-icon'
}

function FactorAction({
  kind,
  totp,
  disabled,
  method,
  controller,
}: Readonly<{
  kind: ReturnType<typeof methodKind>
  totp: boolean
  disabled: boolean
  method: FactorMethod
  controller: Controller
}>) {
  const { t } = useTranslation()
  if (kind === 'enable') {
    return (
      <button
        type="button"
        className={totp ? 'fx-acc2-btn' : 'fx-acc2-btn-outline'}
        disabled={disabled}
        onClick={() => void controller.begin(method)}
      >
        {t(totp ? 'twofa.enable_app' : 'twofa.enable_email')}
      </button>
    )
  }
  if (kind === 'disable') {
    return (
      <button
        type="button"
        className="fx-acc2-btn-danger"
        disabled={disabled}
        onClick={() => void controller.turnOff(method)}
      >
        {t(totp ? 'twofa.disable' : 'twofa.disable_email')}
      </button>
    )
  }
  return null
}

function FactorRow({
  method,
  kind,
  controller,
}: Readonly<{
  method: MethodSnapshot
  kind: ReturnType<typeof methodKind>
  controller: Controller
}>) {
  const { t } = useTranslation()
  const totp = method.id === 'totp'
  const disabled = methodActionDisabled(kind, controller.password, controller.code, controller.busy)
  const name = t(totp ? 'twofa.method_app' : 'twofa.method_email')
  let action: ReactNode
  if (kind === 'enable' || kind === 'disable') {
    action = (
      <FactorAction
        kind={kind}
        totp={totp}
        disabled={disabled}
        method={method.id as FactorMethod}
        controller={controller}
      />
    )
  }
  return (
    <MethodLine
      icon={totp ? I.key : I.mail}
      tone={method.enabled ? 'on' : undefined}
      name={name}
      hint={t(totp ? 'twofa.method_app_hint' : 'twofa.method_email_hint')}
      state={method.enabled ? t('twofa.state_active') : t('twofa.state_off')}
      stateOn={method.enabled}
      /*
        The lock sits on the METHOD it blocks, and only when that method is
        on. A note at the foot of the card could not say which button was
        missing. `can_disable_*` is also false for a method nobody enrolled,
        so the lock is keyed on methodKind, which already requires it to be on.
        The server refuses removal in exactly one case — admins, when the
        instance requires a second factor — so there is no second reason.
      */
      lock={kind === 'lock' ? t('twofa.required_note') : undefined}
      note={kind === 'unavailable' ? t('twofa.email_unavailable') : undefined}
      action={action}
    />
  )
}

function MethodLine({
  icon,
  tone,
  name,
  hint,
  state,
  stateOn,
  lock,
  note,
  action,
}: Readonly<{
  icon: ReactNode
  tone?: 'on' | 'warn'
  name: string
  hint: string
  state?: string
  stateOn?: boolean
  lock?: string
  note?: string
  action?: ReactNode
}>) {
  const iconClass = methodIconClass(tone)
  return (
    <div className="fx-acc2-row" role="group" aria-label={name}>
      <span className={iconClass} aria-hidden="true">
        <Icon d={icon} size={15} />
      </span>
      <div className="fx-acc2-row-main">
        <div className="fx-acc2-row-title">{name}</div>
        <div className="fx-acc2-row-sub">{hint}</div>
        {lock && (
          <span className="fx-acc2-method-lock">
            <Icon d={I.lock} size={11} /> {lock}
          </span>
        )}
        {note && <span className="fx-acc2-method-note">{note}</span>}
      </div>
      {state && (
        <span className={stateOn ? 'fx-acc2-pill fx-acc2-pill-ok' : 'fx-acc2-pill'}>{state}</span>
      )}
      {action && <div className="fx-acc2-row-actions">{action}</div>}
    </div>
  )
}

/* ─── enrollment ────────────────────────────────────────────────────── */

function EnrollmentPanel({ controller }: Readonly<{ controller: Controller }>) {
  const { t } = useTranslation()
  const enrollment = controller.enrollment
  if (!enrollment) return null
  return (
    <div className="fx-acc2-card fx-acc2-2fa-panel">
      <div className="fx-acc2-card-body">
        <div>
          <div className="fx-acc2-panel-title">{t('twofa.enroll_title')}</div>
          <span className="fx-acc2-field-hint">
            {enrollment.method === 'totp'
              ? t('twofa.enroll_subtitle')
              : t('twofa.enroll_email_subtitle', { account: enrollment.email.account })}
          </span>
        </div>
        {controller.error && <Notice tone="bad">{controller.error}</Notice>}
        {enrollment.method === 'totp' && (
          <div className="fx-2fa-enroll">
            <div className="fx-authfield">
              <div className="fx-auth-qr">
                <img src={enrollment.totp.qr_url} alt={t('twofa.qr_alt')} width={240} height={240} />
              </div>
            </div>
            <div className="fx-2fa-key">
              <span className="fx-acc2-field-label">{t('twofa.setup_key')}</span>
              <code className="fx-2fa-key-value" translate="no">{enrollment.totp.secret}</code>
            </div>
          </div>
        )}
        <label className="fx-acc2-field">
          <span className="fx-acc2-field-label">{t('twofa.current_code')}</span>
          <div className="fx-authfield fx-2fa-otp">
            <OtpInput value={controller.code} onChange={controller.setCode} disabled={controller.busy} />
          </div>
        </label>
        <div className="fx-acc2-row-actions">
          <button
            type="button"
            className="fx-acc2-btn"
            disabled={controller.busy || controller.code.length < OTP_LENGTH}
            onClick={() => void controller.confirm()}
          >
            {t('twofa.confirm')}
          </button>
          <button type="button" className="fx-acc2-btn-outline" disabled={controller.busy} onClick={controller.reset}>
            {t('common.cancel')}
          </button>
        </div>
      </div>
    </div>
  )
}

function RecoveryCodesPanel({ controller }: Readonly<{ controller: Controller }>) {
  const { t } = useTranslation()
  return (
    <div className="fx-acc2-card fx-acc2-2fa-panel">
      <div className="fx-acc2-card-body">
        <div>
          <div className="fx-acc2-panel-title">{t('twofa.codes_title')}</div>
          <span className="fx-acc2-field-hint">{t('twofa.codes_subtitle')}</span>
        </div>
        <div className="fx-authfield">
          <RecoveryCodes codes={controller.codes ?? []} onDone={controller.dismissCodes} />
        </div>
      </div>
    </div>
  )
}

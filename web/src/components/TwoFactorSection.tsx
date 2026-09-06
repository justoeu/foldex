import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { OtpInput, OTP_LENGTH } from './auth/OtpInput'
import { RecoveryCodes } from './auth/RecoveryCodes'
import { useTwoFactorController } from '../hooks/useTwoFactorController'
import { PasswordInput } from './PasswordInput'
import { MailCodeHint } from './account/MailCodeButton'
import {
  Notice,
  SectionBadge,
  SectionBlock,
  SectionCard,
  SectionRow,
} from './account/SectionCard'
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

function TwoFactorOverview({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  return (
    <SectionCard
      icon={I.shield}
      title={controller.enabled ? t('twofa.status_on') : t('twofa.status_off')}
      subtitle={t('twofa.section_desc')}
      badge={
        <SectionBadge tone={controller.enabled ? 'on' : 'off'}>
          {controller.enabled ? t('twofa.badge_on') : t('twofa.badge_off')}
        </SectionBadge>
      }
    >
      {controller.error && <Notice tone="bad">{controller.error}</Notice>}
      {/*
        The proof comes BEFORE the actions it unlocks. Every button below is
        disabled until these two fields are filled, and with the fields
        underneath them the screen read as four broken controls and a form with
        no stated purpose — which is exactly how it was reported.
      */}
      <ProofPanel controller={controller} />
      <MethodList controller={controller} />
    </SectionCard>
  )
}

function ProofPanel({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  return (
    <div className="fx-2fa-proof">
      <div className="fx-2fa-proof-head">
        <Icon d={I.lock} size={13} />
        <span className="fx-sec-block-label">{t('twofa.proof_label')}</span>
      </div>
      <p className="fx-2fa-proof-hint">
        {controller.enabled ? t('twofa.proof_hint') : t('twofa.proof_hint_password')}
      </p>
      <label className="fx-field">
        <span className="fx-field-label">{t('twofa.current_password')}</span>
        <PasswordInput
          className="fx-input"
          autoComplete="current-password"
          value={controller.password}
          onChange={(event) => controller.setPassword(event.target.value)}
        />
      </label>
      {controller.enabled && (
        <label className="fx-field">
          <span className="fx-field-label">{t('twofa.current_code')}</span>
          <div className="fx-authfield fx-2fa-otp">
            <OtpInput value={controller.code} onChange={controller.setCode} disabled={controller.busy} />
          </div>
          <ProofHint controller={controller} />
        </label>
      )}
    </div>
  )
}

/**
 * Says which code the field will accept, and offers to mail one.
 *
 * Not decoration: an account whose only factor is e-mail has no authenticator
 * to read a code from, and without this the field is a box it cannot fill.
 */
function ProofHint({ controller }: { controller: Controller }) {
  if (!controller.emailEnabled) return null
  return (
    <MailCodeHint
      sent={controller.codeSent}
      busy={controller.busy}
      onSend={() => void controller.mailStepUpCode()}
    />
  )
}

function MethodList({ controller }: { controller: Controller }) {
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
    <SectionBlock label={t('twofa.methods_label')}>
      <div className="fx-sec-rows">
        {methods.map((method) => (
          <MethodRow key={method.id} method={method} controller={controller} />
        ))}
      </div>
    </SectionBlock>
  )
}

function MethodRow({
  method,
  controller,
}: {
  method: MethodSnapshot
  controller: Controller
}) {
  const kind = methodKind(method)
  if (kind === 'hidden') return null
  if (method.id === 'recovery') {
    return <RecoveryRow controller={controller} />
  }
  return <FactorRow method={method} kind={kind} controller={controller} />
}

function RecoveryRow({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  const low = controller.remaining < LOW_RECOVERY_CODES
  return (
    <SectionRow
      icon={I.key}
      /* Two encodings, deliberately: the count is easy to read past, and
         running out of recovery codes is only discovered when they are
         already needed. */
      tone={low ? 'warn' : undefined}
      name={t('twofa.remaining', { count: controller.remaining })}
      hint={low ? t('twofa.recovery_low') : t('twofa.recovery_hint')}
      action={
        <button
          className="fx-btn"
          disabled={methodActionDisabled('regenerate', controller.password, controller.code, controller.busy)}
          onClick={() => void controller.regenerate()}
        >
          {t('twofa.regenerate')}
        </button>
      }
    />
  )
}

function FactorRow({
  method,
  kind,
  controller,
}: {
  method: MethodSnapshot
  kind: ReturnType<typeof methodKind>
  controller: Controller
}) {
  const { t } = useTranslation()
  const totp = method.id === 'totp'
  const disabled = methodActionDisabled(kind, controller.password, controller.code, controller.busy)
  return (
    <SectionRow
      icon={totp ? I.key : I.mail}
      name={t(totp ? 'twofa.method_app' : 'twofa.method_email')}
      hint={t(totp ? 'twofa.method_app_hint' : 'twofa.method_email_hint')}
      tone={method.enabled ? 'on' : undefined}
      state={{
        label: method.enabled ? t('twofa.state_active') : t('twofa.state_off'),
        on: method.enabled,
      }}
      /*
        The lock is shown against the METHOD it applies to, and only when
        that method is on. A note at the foot of the card explained nothing
        about which of the two buttons was missing, and a missing button
        with no explanation beside it reads as a broken screen.

        The enrolled half is load-bearing here: `can_disable_*` is also false
        for a method nobody enrolled, so a lock keyed on it alone would claim
        every unused method is protected. methodKind already folds that in.

        One reason, not a ternary. The server refuses a removal in exactly
        one case — `mayRemoveFactor` returns false only under
        `require2FAForAdmins && role.IsAdmin()`, which is what `required`
        already reports — so a second arm could never render, and the copy
        it would have carried ("this is your only method") asserts a
        last-factor guard that does not exist: an ordinary user may remove
        their last one freely.
      */
      lock={kind === 'lock' ? t('twofa.required_note') : undefined}
      /*
        An instance whose mail driver prints to stdout refuses this
        enrollment, so the row says so instead of offering a button the
        backend would always reject.
      */
      note={kind === 'unavailable' ? t('twofa.email_unavailable') : undefined}
      action={
        <>
          {kind === 'enable' && (
            <button
              className={totp ? 'fx-btn fx-btn-primary' : 'fx-btn'}
              disabled={disabled}
              onClick={() => void controller.begin(method.id as FactorMethod)}
            >
              {t(totp ? 'twofa.enable_app' : 'twofa.enable_email')}
            </button>
          )}
          {kind === 'disable' && (
            <button
              className="fx-btn fx-btn-danger"
              disabled={disabled}
              onClick={() => void controller.turnOff(method.id as FactorMethod)}
            >
              {t(totp ? 'twofa.disable' : 'twofa.disable_email')}
            </button>
          )}
        </>
      }
    />
  )
}

/* ─── enrollment ────────────────────────────────────────────────────── */

function EnrollmentPanel({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  const enrollment = controller.enrollment
  if (!enrollment) return null
  return (
    <SectionCard
      icon={I.shield}
      title={t('twofa.enroll_title')}
      subtitle={
        enrollment.method === 'totp'
          ? t('twofa.enroll_subtitle')
          : t('twofa.enroll_email_subtitle', { account: enrollment.email.account })
      }
    >
      {controller.error && <Notice tone="bad">{controller.error}</Notice>}
      {enrollment.method === 'totp' && (
        <div className="fx-2fa-enroll">
          <div className="fx-authfield">
            <div className="fx-auth-qr">
              <img src={enrollment.totp.qr_url} alt={t('twofa.qr_alt')} width={240} height={240} />
            </div>
          </div>
          <div className="fx-2fa-key">
            <span className="fx-sec-block-label">{t('twofa.setup_key')}</span>
            <code className="fx-2fa-key-value" translate="no">{enrollment.totp.secret}</code>
          </div>
        </div>
      )}
      <label className="fx-field">
        <span className="fx-field-label">{t('twofa.current_code')}</span>
        <div className="fx-authfield fx-2fa-otp">
          <OtpInput value={controller.code} onChange={controller.setCode} disabled={controller.busy} />
        </div>
      </label>
      <div className="fx-sec-actions">
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
      </div>
    </SectionCard>
  )
}

function RecoveryCodesPanel({ controller }: { controller: Controller }) {
  const { t } = useTranslation()
  return (
    <SectionCard icon={I.key} title={t('twofa.codes_title')} subtitle={t('twofa.codes_subtitle')}>
      <div className="fx-authfield">
        <RecoveryCodes codes={controller.codes ?? []} onDone={controller.dismissCodes} />
      </div>
    </SectionCard>
  )
}

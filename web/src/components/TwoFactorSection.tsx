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

type Controller = ReturnType<typeof useTwoFactorController>

/** Below this, the band turns amber and says so. */
const LOW_RECOVERY_CODES = 3

/**
 * The step-up the destructive actions require: the same two proofs that turned
 * the factor on.
 *
 * Derived in one place because two components gate on it — the method rows and
 * the recovery band — and an identical expression copied into both drifts in
 * the direction nobody notices: the UI would offer one operation and refuse the
 * other, for a rule that is supposed to be the same rule.
 */
function proofMissing(controller: Controller): boolean {
  return !controller.password || controller.code.length < OTP_LENGTH
}

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
  const missing = proofMissing(controller)
  const low = controller.remaining < LOW_RECOVERY_CODES
  return (
    <SectionBlock label={t('twofa.methods_label')}>
      <div className="fx-sec-rows">
        <SectionRow
          icon={I.key}
          name={t('twofa.method_app')}
          hint={t('twofa.method_app_hint')}
          tone={controller.totpEnabled ? 'on' : undefined}
          state={{
            label: controller.totpEnabled ? t('twofa.state_active') : t('twofa.state_off'),
            on: controller.totpEnabled,
          }}
          /*
            The lock is shown against the METHOD it applies to, and only when
            that method is on. A note at the foot of the card explained nothing
            about which of the two buttons was missing, and a missing button
            with no explanation beside it reads as a broken screen.

            The enrolled half is load-bearing here, unlike on the button below:
            `can_disable_*` is also false for a method nobody enrolled, so a
            lock keyed on it alone would claim every unused method is protected.

            One reason, not a ternary. The server refuses a removal in exactly
            one case — `mayRemoveFactor` returns false only under
            `require2FAForAdmins && role.IsAdmin()`, which is what `required`
            already reports — so a second arm could never render, and the copy
            it would have carried ("this is your only method") asserts a
            last-factor guard that does not exist: an ordinary user may remove
            their last one freely.
          */
          lock={
            controller.totpEnabled && !controller.canDisableTotp
              ? t('twofa.required_note')
              : undefined
          }
          action={
            <>
              {!controller.totpEnabled && (
                <button
                  className="fx-btn fx-btn-primary"
                  disabled={controller.busy || !controller.password}
                  onClick={() => void controller.begin('totp')}
                >
                  {t('twofa.enable_app')}
                </button>
              )}
              {/*
                Enrolled AND removable. The server already folds the first half
                in (`can_disable_totp` is `TOTPEnabled && mayRemoveFactor(…)`),
                so this is a restatement, not a second opinion — and it stays
                because the two buttons share ONE row: were that ever to drift,
                the row would render "set up" and "turn off" side by side,
                describing a state that cannot exist.
              */}
              {controller.totpEnabled && controller.canDisableTotp && (
                <button
                  className="fx-btn fx-btn-danger"
                  disabled={controller.busy || missing}
                  onClick={() => void controller.turnOff('totp')}
                >
                  {t('twofa.disable')}
                </button>
              )}
            </>
          }
        />
        <SectionRow
          icon={I.mail}
          name={t('twofa.method_email')}
          hint={t('twofa.method_email_hint')}
          tone={controller.emailEnabled ? 'on' : undefined}
          state={{
            label: controller.emailEnabled ? t('twofa.state_active') : t('twofa.state_off'),
            on: controller.emailEnabled,
          }}
          lock={
            controller.emailEnabled && !controller.canDisableEmail
              ? t('twofa.required_note')
              : undefined
          }
          /*
            An instance whose mail driver prints to stdout refuses this
            enrollment, so the row says so instead of offering a button the
            backend would always reject.
          */
          note={
            !controller.emailEnabled && !controller.emailAvailable
              ? t('twofa.email_unavailable')
              : undefined
          }
          action={
            <>
              {!controller.emailEnabled && controller.emailAvailable && (
                <button
                  className="fx-btn"
                  disabled={controller.busy || !controller.password}
                  onClick={() => void controller.begin('email')}
                >
                  {t('twofa.enable_email')}
                </button>
              )}
              {controller.emailEnabled && controller.canDisableEmail && (
                <button
                  className="fx-btn fx-btn-danger"
                  disabled={controller.busy || missing}
                  onClick={() => void controller.turnOff('email')}
                >
                  {t('twofa.disable_email')}
                </button>
              )}
            </>
          }
        />
        {controller.enabled && (
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
                disabled={controller.busy || missing}
                onClick={() => void controller.regenerate()}
              >
                {t('twofa.regenerate')}
              </button>
            }
          />
        )}
      </div>
    </SectionBlock>
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
            <code className="fx-2fa-key-value">{enrollment.totp.secret}</code>
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

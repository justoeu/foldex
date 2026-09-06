import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { I } from '../icons'
import { Notice, SectionRow } from './SectionCard'
import { OtpInput } from '../auth/OtpInput'
import { PasswordInput } from '../PasswordInput'
import { PasswordStrength } from '../PasswordStrength'
import * as auth from '../../api/auth'
import { MailCodeButton } from './MailCodeButton'
import { accountErrorMessage } from './accountErrors'
import { useAuth } from '../../auth/AuthProvider'
import { canMailStepUpCode, hasSecondFactor, type AuthUser } from '../../auth/types'
import { usePasswordFloor } from '../../hooks/useInstancePolicy'
import { canSubmit, passwordMode, passwordsMismatch } from './PasswordCard.submit'

/**
 * The account's password: change it when there is one, create one when there
 * is not.
 *
 * PasswordRow only picks which form to mount — `user.has_password` is a
 * property of the account rather than a choice the user makes. Changing
 * proves the CURRENT password (INV-147); creating cannot — a Google-only
 * account has none — so it falls back to the second factor.
 */
export function PasswordRow({ user }: { user: AuthUser }) {
  const { t } = useTranslation()
  const hasPassword = user.has_password
  const mode = passwordMode(hasPassword)

  const [open, setOpen] = useState(false)
  const [done, setDone] = useState(false)

  function close() {
    setOpen(false)
  }

  function finish() {
    setOpen(false)
    setDone(true)
  }

  return (
    <SectionRow
      icon={I.lock}
      name={t('account.password_label')}
      hint={hasPassword ? t('account.password_on') : t('account.password_off')}
      tone={hasPassword ? 'on' : undefined}
      state={{
        label: hasPassword ? t('account.state_set') : t('account.state_unset'),
        on: hasPassword,
      }}
      action={
        <button
          className="fx-btn"
          aria-expanded={open}
          onClick={() => {
            setDone(false)
            setOpen((v) => !v)
          }}
        >
          {open
            ? t('common.cancel')
            : mode === 'change'
              ? t('account.change_password')
              : t('account.set_password')}
        </button>
      }
    >
      {done && !open && (
        <Notice tone="ok">
          {mode === 'change' ? t('account.password_changed') : t('account.password_set_done')}
        </Notice>
      )}

      {open &&
        (mode === 'change' ? (
          <ChangePasswordForm onDone={finish} onCancel={close} />
        ) : (
          <SetPasswordForm user={user} onDone={finish} onCancel={close} />
        ))}
    </SectionRow>
  )
}

function ChangePasswordForm({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const { t } = useTranslation()
  const { reload } = useAuth()
  const minLen = usePasswordFloor()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const blocked =
    busy ||
    !canSubmit({
      hasPassword: true,
      needsStepUp: false,
      current,
      next,
      confirm,
      code: '',
      minLen,
    })

  async function submit() {
    if (passwordsMismatch(next, confirm)) {
      setError(t('auth_errors.password_mismatch'))
      return
    }
    setBusy(true)
    setError('')
    try {
      await auth.changePassword(current, next)
      onDone()
      await reload()
    } catch (e) {
      setError(accountErrorMessage(e, t, minLen))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      {error && <Notice tone="bad">{error}</Notice>}
      <label className="fx-field">
        <span className="fx-field-label">{t('account.current_password')}</span>
        <PasswordInput
          className="fx-input"
          autoComplete="current-password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
        />
      </label>
      <NewPasswordFields next={next} confirm={confirm} onNext={setNext} onConfirm={setConfirm} />
      <FormActions blocked={blocked} onSubmit={submit} onCancel={onCancel} note={t('account.change_password_note')} />
    </>
  )
}

function SetPasswordForm({
  user,
  onDone,
  onCancel,
}: {
  user: AuthUser
  onDone: () => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const { reload } = useAuth()
  const minLen = usePasswordFloor()
  const needsStepUp = hasSecondFactor(user)
  const canMailCode = canMailStepUpCode(user)

  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const blocked =
    busy ||
    !canSubmit({
      hasPassword: false,
      needsStepUp,
      current: '',
      next,
      confirm,
      code,
      minLen,
    })

  async function submit() {
    if (passwordsMismatch(next, confirm)) {
      setError(t('auth_errors.password_mismatch'))
      return
    }
    setBusy(true)
    setError('')
    try {
      await auth.setPassword(next, needsStepUp ? code : undefined)
      onDone()
      // has_password flips on the create branch, and the hero reads it.
      await reload()
    } catch (e) {
      // Clear only the CODE. The passwords stay so a rejected step-up does not
      // cost the user the whole form, but a spent code resubmitted verbatim
      // burns another attempt from the server's budget — and on the e-mail
      // path, another message.
      setCode('')
      setError(accountErrorMessage(e, t, minLen))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      {error && <Notice tone="bad">{error}</Notice>}
      <Notice tone="info">{t('account.password_why')}</Notice>
      <NewPasswordFields next={next} confirm={confirm} onNext={setNext} onConfirm={setConfirm} />
      {needsStepUp && (
        <label className="fx-field">
          <span className="fx-field-label">{t('account.current_code')}</span>
          <div className="fx-authfield">
            <OtpInput value={code} onChange={setCode} disabled={busy} />
          </div>
          {canMailCode && <MailCodeButton disabled={busy} />}
        </label>
      )}
      <FormActions blocked={blocked} onSubmit={submit} onCancel={onCancel} note={t('account.set_password_note')} />
    </>
  )
}

function NewPasswordFields({
  next,
  confirm,
  onNext,
  onConfirm,
}: {
  next: string
  confirm: string
  onNext: (value: string) => void
  onConfirm: (value: string) => void
}) {
  const { t } = useTranslation()
  return (
    <>
      <label className="fx-field">
        <span className="fx-field-label">{t('account.new_password')}</span>
        <PasswordInput
          className="fx-input"
          autoComplete="new-password"
          value={next}
          onChange={(e) => onNext(e.target.value)}
        />
      </label>
      <PasswordStrength value={next} />
      <label className="fx-field">
        <span className="fx-field-label">{t('account.confirm_password')}</span>
        <PasswordInput
          className="fx-input"
          autoComplete="new-password"
          value={confirm}
          onChange={(e) => onConfirm(e.target.value)}
        />
      </label>
    </>
  )
}

function FormActions({
  blocked,
  onSubmit,
  onCancel,
  note,
}: {
  blocked: boolean
  onSubmit: () => void
  onCancel: () => void
  note: string
}) {
  const { t } = useTranslation()
  return (
    <>
      <div className="fx-sec-actions">
        <button className="fx-btn fx-btn-primary" disabled={blocked} onClick={() => void onSubmit()}>
          {t('account.save_password')}
        </button>
        <button className="fx-btn" onClick={onCancel}>
          {t('common.cancel')}
        </button>
      </div>
      <span className="fx-sec-row-hint">{note}</span>
    </>
  )
}

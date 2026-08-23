import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { I } from '../icons'
import { Notice, SectionRow } from './SectionCard'
import { OtpInput, OTP_LENGTH } from '../auth/OtpInput'
import { PasswordInput } from '../PasswordInput'
import { PasswordStrength } from '../PasswordStrength'
import * as auth from '../../api/auth'
import { MailCodeButton } from './MailCodeButton'
import { accountErrorMessage } from './accountErrors'
import { useAuth } from '../../auth/AuthProvider'
import { MIN_PASSWORD_LEN, canMailStepUpCode, hasSecondFactor, type AuthUser } from '../../auth/types'

/**
 * The account's password: change it when there is one, create one when there
 * is not.
 *
 * The two are one component because they are the same field with a different
 * proof, and which one applies is a property of the account rather than a
 * choice the user makes. Changing proves the CURRENT password; creating cannot
 * — a Google-only account has none — so it falls back to the second factor,
 * which is why `code` appears only on that branch.
 *
 * Until this shipped there was no way to change a password while signed in at
 * all: `POST /api/auth/password/change` existed, was tested, and nothing in the
 * SPA called it. The route out was to sign out and use "forgot password", which
 * is a recovery flow standing in for an ordinary edit.
 */
export function PasswordRow({ user }: { user: AuthUser }) {
  const { t } = useTranslation()
  const { reload } = useAuth()
  const hasPassword = user.has_password

  const [open, setOpen] = useState(false)
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)
  const [busy, setBusy] = useState(false)

  // The AGGREGATE, not the authenticator alone — see hasSecondFactor.
  const needsStepUp = hasSecondFactor(user)
  const canMailCode = canMailStepUpCode(user)

  function reset() {
    setCurrent('')
    setNext('')
    setConfirm('')
    setCode('')
    setError('')
  }

  async function submit() {
    if (next !== confirm) {
      setError(t('auth_errors.password_mismatch'))
      return
    }
    setBusy(true)
    setError('')
    try {
      if (hasPassword) await auth.changePassword(current, next)
      else await auth.setPassword(next, needsStepUp ? code : undefined)
      reset()
      setOpen(false)
      setDone(true)
      // has_password flips on the create branch, and the hero reads it.
      await reload()
    } catch (e) {
      // Clear only the CODE. The passwords stay so a rejected step-up does not
      // cost the user the whole form, but a spent code resubmitted verbatim
      // burns another attempt from the server's budget — and on the e-mail
      // path, another message.
      setCode('')
      setError(accountErrorMessage(e, t))
    } finally {
      setBusy(false)
    }
  }

  const tooShort = next.length < MIN_PASSWORD_LEN
  const blocked =
    busy ||
    tooShort ||
    !confirm ||
    (hasPassword && !current) ||
    (!hasPassword && needsStepUp && code.length < OTP_LENGTH)

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
            reset()
            setOpen((v) => !v)
          }}
        >
          {open
            ? t('common.cancel')
            : hasPassword
              ? t('account.change_password')
              : t('account.set_password')}
        </button>
      }
    >
      {done && !open && (
        <Notice tone="ok">
          {hasPassword ? t('account.password_changed') : t('account.password_set_done')}
        </Notice>
      )}

      {open && (
        <>
          {/* The refusal leads the form. Below the fields it was a 12px line
              under a button the user had already stopped looking at. */}
          {error && <Notice tone="bad">{error}</Notice>}

          {!hasPassword && <Notice tone="info">{t('account.password_why')}</Notice>}

          {hasPassword && (
            <label className="fx-field">
              <span className="fx-field-label">{t('account.current_password')}</span>
              <PasswordInput
                className="fx-input"
                autoComplete="current-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
              />
            </label>
          )}

          <label className="fx-field">
            <span className="fx-field-label">{t('account.new_password')}</span>
            <PasswordInput
              className="fx-input"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
          </label>
          <PasswordStrength value={next} />

          <label className="fx-field">
            <span className="fx-field-label">{t('account.confirm_password')}</span>
            <PasswordInput
              className="fx-input"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
          </label>

          {/* Only on the CREATE branch: with no current password to prove, the
              second factor is the only step-up available, and this credential
              outlives the session presenting the request. */}
          {!hasPassword && needsStepUp && (
            <label className="fx-field">
              <span className="fx-field-label">{t('account.current_code')}</span>
              <div className="fx-authfield">
                <OtpInput value={code} onChange={setCode} disabled={busy} />
              </div>
              {canMailCode && <MailCodeButton disabled={busy} />}
            </label>
          )}

          <div className="fx-sec-actions">
            <button className="fx-btn fx-btn-primary" disabled={blocked} onClick={() => void submit()}>
              {t('account.save_password')}
            </button>
            <button
              className="fx-btn"
              onClick={() => {
                reset()
                setOpen(false)
              }}
            >
              {t('common.cancel')}
            </button>
          </div>
          <span className="fx-sec-row-hint">
            {hasPassword ? t('account.change_password_note') : t('account.set_password_note')}
          </span>
        </>
      )}
    </SectionRow>
  )
}

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { I } from '../icons'
import { Notice, SectionRow } from './SectionCard'
import { PasswordInput } from '../PasswordInput'
import * as auth from '../../api/auth'
import { apiErrorCode as errCode } from '../../lib/apiError'
import type { AuthUser } from '../../auth/types'

/**
 * The account's e-mail, and the two-step that moves it.
 *
 * The address is not edited in place. It is the login identifier AND the
 * recovery channel, so writing a new one straight in would make a typo both —
 * with the warning going to the address that was typed wrong. Instead the new
 * mailbox gets a link, the current one keeps working until that link is opened,
 * and the OLD address gets a linkless warning so someone being taken over hears
 * about it on the channel they still control.
 */
export function EmailRow({ user }: { user: AuthUser }) {
  const { t } = useTranslation()
  const qc = useQueryClient()

  const pending = useQuery({ queryKey: ['email-change'], queryFn: auth.fetchEmailChange })
  const [open, setOpen] = useState(false)
  const [next, setNext] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')

  const request = useMutation({
    mutationFn: () => auth.requestEmailChange(next.trim(), password),
    onSuccess: async () => {
      setOpen(false)
      setNext('')
      setPassword('')
      setError('')
      await qc.invalidateQueries({ queryKey: ['email-change'] })
    },
    onError: (e) => {
      // Only the PASSWORD is cleared. The address stays so a wrong password
      // does not cost the user the whole form, but a password left in a field
      // after a refusal is one browser autofill away from being resubmitted.
      setPassword('')
      setError(messageFor(e, t))
    },
  })

  const cancel = useMutation({
    mutationFn: auth.cancelEmailChange,
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['email-change'] })
    },
  })

  // `data` is undefined BOTH while the query runs and when there is no pending
  // change, and conflating the two flips the button's label a moment after
  // mount — worse, a user who clicks in that window opens the form and watches
  // it vanish, discarding the address and password they had already typed.
  const loading = pending.isLoading
  const live = pending.data
  return (
    <SectionRow
      icon={I.mail}
      name={t('account.email_label')}
      hint={user.email}
      tone={live ? 'warn' : undefined}
      state={
        user.email_verified_at
          ? { label: t('account.state_verified'), on: true }
          : { label: t('account.state_unverified'), on: false }
      }
      action={
        loading ? null : live ? (
          <button className="fx-btn" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
            {t('account.email_change_cancel')}
          </button>
        ) : (
          <button
            className="fx-btn"
            aria-expanded={open}
            onClick={() => {
              setError('')
              setOpen((v) => !v)
            }}
          >
            {open ? t('common.cancel') : t('account.change_email')}
          </button>
        )
      }
    >
      {/* A live request outranks the form: the next useful action is opening
          the link, or cancelling — not typing another address. */}
      {live && <Notice tone="info">{t('account.email_change_sent', { email: live.new_email })}</Notice>}

      {!loading && !live && open && (
        <>
          {error && <Notice tone="bad">{error}</Notice>}
          <Notice tone="info">{t('account.email_change_why')}</Notice>

          <label className="fx-field">
            <span className="fx-field-label">{t('account.email_new')}</span>
            <div className="fx-input">
              <input
                type="email"
                autoComplete="email"
                value={next}
                onChange={(e) => setNext(e.target.value)}
                aria-label={t('account.email_new')}
              />
            </div>
          </label>

          {/* The step-up. Without it a stolen session moves the account's
              recovery channel to an address the attacker owns. */}
          <label className="fx-field">
            <span className="fx-field-label">{t('account.current_password')}</span>
            <PasswordInput
              className="fx-input"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>

          <div className="fx-sec-actions">
            <button
              className="fx-btn fx-btn-primary"
              disabled={!next.trim() || !password || request.isPending}
              onClick={() => request.mutate()}
            >
              {t('account.email_change_send')}
            </button>
          </div>
        </>
      )}
    </SectionRow>
  )
}

function messageFor(err: unknown, t: (k: string) => string): string {
  switch (errCode(err)) {
    case 'email_taken':
      return t('account.err_email_taken')
    case 'email_unchanged':
      return t('account.err_email_unchanged')
    case 'invalid_email':
      return t('account.err_email_invalid')
    case 'mail_unavailable':
      return t('account.err_mail_unavailable')
    case 'invalid_credentials':
      return t('account.wrong_password')
    case 'too_many_attempts':
      return t('auth_errors.too_many_attempts')
    default:
      return t('auth_errors.generic')
  }
}

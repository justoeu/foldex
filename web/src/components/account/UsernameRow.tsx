import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation } from '@tanstack/react-query'
import { I } from '../icons'
import { Notice, SectionRow } from './SectionCard'
import { AvailabilityHint } from '../AvailabilityHint'
import { useAvailability } from '../../hooks/useAvailability'
import * as auth from '../../api/auth'
import { useAuth } from '../../auth/AuthProvider'
import { apiErrorCode as errCode } from '../../lib/apiError'
import type { AuthUser } from '../../auth/types'

/** Mirrors the server's shape rule, so an obviously-wrong value is refused
 *  before a round-trip. The server and the database CHECK remain the
 *  authorities — this only saves the user a wait. */
export const MAX_USERNAME = 32

/**
 * The username, as a way to SIGN IN.
 *
 * It lived on the profile form for one session, beside the display name and
 * the language, and that was the wrong place: those two are preferences, and
 * this is an identifier — you type it into the login screen exactly where the
 * e-mail goes. Filed under "profile" it read as a nickname, which is also why
 * nobody would think to look for it here.
 *
 * It writes through `updateUsername`, which sends the username ALONE. Every
 * field on that endpoint is tri-state, so a row that replayed the cached
 * display name would revert a rename made in another tab.
 */
export function UsernameRow({ user }: { user: AuthUser }) {
  const { t } = useTranslation()
  const { adopt } = useAuth()

  const current = user.username ?? ''
  const [open, setOpen] = useState(false)
  const [value, setValue] = useState(current)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  const save = useMutation({
    mutationFn: (next: string) => auth.updateUsername(next),
    onSuccess: (me) => {
      setError('')
      setSaved(true)
      setOpen(false)
      adopt(me)
    },
    onError: (e) => {
      setSaved(false)
      const code = errCode(e)
      if (code === 'invalid_username') setError(t('profile.err_username_shape'))
      else if (code === 'username_taken') setError(t('profile.err_username_taken'))
      else setError(t('auth_errors.generic'))
    },
  })

  const next = value.trim()
  // Asked while typing, and a REFUSAL gates the save. Only a refusal: blocking
  // while the probe is in flight leaves the button dead for the debounce, so
  // someone who types fast and clicks immediately finds it greyed out for no
  // reason they can see. A click during the check reaches the server, which
  // refuses it — the same answer, from the authority that was always going to
  // give it.
  const avail = useAvailability(auth.usernameAvailable, value, current)
  const blocked = avail.state === 'refused'
  return (
    <SectionRow
      icon={I.user}
      name={t('profile.username_label')}
      hint={current || t('account.username_none')}
      state={
        current
          ? { label: t('account.state_set'), on: true }
          : { label: t('account.state_unset'), on: false }
      }
      action={
        <button
          className="fx-btn"
          aria-expanded={open}
          onClick={() => {
            setError('')
            setSaved(false)
            setValue(current)
            setOpen((v) => !v)
          }}
        >
          {open ? t('common.cancel') : current ? t('common.edit') : t('account.username_set')}
        </button>
      }
    >
      {saved && !open && <Notice tone="ok">{t('account.username_saved')}</Notice>}

      {open && (
        <>
          {error && <Notice tone="bad">{error}</Notice>}
          <Notice tone="info">{t('profile.username_hint')}</Notice>

          <label className="fx-field">
            <span className="fx-field-label">{t('profile.username_label')}</span>
            <div className="fx-input">
              <input
                type="text"
                autoCapitalize="off"
                autoCorrect="off"
                spellCheck={false}
                autoComplete="username"
                maxLength={MAX_USERNAME}
                value={value}
                onChange={(e) => {
                  setValue(e.target.value)
                  setError('')
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && next !== current && !save.isPending && !blocked) {
                    e.preventDefault()
                    save.mutate(next)
                  }
                }}
                placeholder={t('profile.username_placeholder')}
                aria-label={t('profile.username_label')}
              />
            </div>
            <AvailabilityHint result={avail} shapeText={t('profile.err_username_shape')} />
          </label>

          <div className="fx-sec-actions">
            {/* Clearing is the other half of "optional": a username you cannot
                remove is not optional, it is merely deferred. */}
            {current && (
              <button
                className="fx-btn fx-btn-danger"
                disabled={save.isPending}
                onClick={() => save.mutate('')}
              >
                {t('common.remove')}
              </button>
            )}
            <button
              className="fx-btn fx-btn-primary"
              disabled={next === current || save.isPending || blocked}
              onClick={() => save.mutate(next)}
            >
              {t('common.save')}
            </button>
          </div>
        </>
      )}
    </SectionRow>
  )
}

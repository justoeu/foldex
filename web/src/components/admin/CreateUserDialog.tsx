import { useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from '../icons'
import { PasswordInput } from '../PasswordInput'
import { PasswordStrength } from '../PasswordStrength'
import { useEscape } from '../../hooks/useEscape'
import { useFocusTrap } from '../../hooks/useFocusTrap'
import { apiErrorCode as errCode, apiErrorMessage } from '../../lib/apiError'
import { MIN_PASSWORD_LEN, type Role } from '../../auth/types'
import * as admin from '../../api/admin'
import { AvailabilityHint } from '../AvailabilityHint'
import { useAvailability } from '../../hooks/useAvailability'

const ASSIGNABLE: readonly Role[] = ['admin', 'editor', 'viewer']

/**
 * Creates an account with a password the administrator types.
 *
 * The warning is not decoration and must not be softened: this is the one place
 * in the product where a credential is known by two people, and the person it
 * belongs to did not choose it. `admin.create_warning` says so, and the dialog
 * points at the invitation as the alternative — the owner asked for this route
 * knowing the trade, and the next administrator to open the dialog did not.
 *
 * `owner` is absent from the role list because the server refuses it: the one
 * role that cannot be demoted is reachable only through an explicit transfer.
 */
export function CreateUserDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const dialogRef = useRef<HTMLDivElement>(null)
  useEscape(onClose)
  useFocusTrap(dialogRef, true)

  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<Role>('editor')
  const [error, setError] = useState('')

  const create = useMutation({
    mutationFn: () => admin.createUser({ email: email.trim(), name: name.trim(), password, role }),
    onSuccess: async () => {
      // The page keys on ['admin', 'users'] / ['admin', 'invites']; invalidating the
      // prefix is what the page's own mutations do.
      await qc.invalidateQueries({ queryKey: ['admin'] })
      onClose()
    },
    onError: (e) => {
      const code = errCode(e)
      if (code === 'email_taken') setError(t('admin.err_email_taken'))
      else if (code === 'invalid_email') setError(t('auth_errors.invalid_email'))
      else if (code === 'password_too_short')
        // The SERVER's message, because the floor is owner-configurable: an
        // instance demanding twenty characters would otherwise be told "at
        // least 8" by a client constant that cannot know better.
        setError(apiErrorMessage(e) ?? t('auth_errors.password_too_short', { count: MIN_PASSWORD_LEN }))
      else if (code === 'invalid_role') setError(t('admin.err_invalid_role'))
      else setError(t('auth_errors.generic'))
    },
  })

  // Safe to ask freely HERE and nowhere else: past RequireAdmin the caller can
  // already list every account with its address, so the probe discloses nothing
  // they could not read directly. The e-mail-CHANGE flow deliberately has no
  // such endpoint — there a password is the cost of each guess.
  const avail = useAvailability(admin.emailAvailable, email)
  const blocked =
    !email.trim() ||
    password.length < MIN_PASSWORD_LEN ||
    create.isPending ||
    // Only a refusal blocks; see UsernameRow for why "checking" must not.
    avail.state === 'refused'

  function submit(e: FormEvent) {
    e.preventDefault()
    if (blocked) return
    setError('')
    create.mutate()
  }

  return (
    <div
      className="fx-drawer-scrim"
      role="dialog"
      aria-modal="true"
      aria-label={t('admin.create_title')}
      // Only a click on the SCRIM itself dismisses; one that started inside the
      // panel and drifted out (selecting text in the warning, say) must not
      // throw away a half-typed credential.
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="fx-drawer" ref={dialogRef}>
        <form onSubmit={submit} style={{ display: 'contents' }}>
          <div className="fx-drawer-head">
            <div>
              <div className="fx-drawer-kicker">{t('admin.create_kicker')}</div>
              <h3 className="fx-drawer-title">{t('admin.create_title')}</h3>
            </div>
            <button
              type="button"
              className="fx-iconbtn"
              aria-label={t('common.close')}
              onClick={onClose}
            >
              <Icon d={I.x} size={15} />
            </button>
          </div>

          <div className="fx-drawer-body">
            <p className="fx-acct-hint">{t('admin.create_warning')}</p>

            {error && (
              <div className="fx-inline-error" role="alert">
                <Icon d={I.alert} size={13} /> {error}
              </div>
            )}

            <label className="fx-field">
              <span className="fx-field-label">{t('auth.email')}</span>
              <div className="fx-input">
                <input type="email" autoComplete="off" value={email} required
                       onChange={(e) => setEmail(e.target.value)}
                       aria-label={t('auth.email')} />
              </div>
              <AvailabilityHint result={avail} />
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('profile.name_label')}</span>
              <div className="fx-input">
                <input type="text" maxLength={120} value={name}
                       onChange={(e) => setName(e.target.value)}
                       aria-label={t('profile.name_label')} />
              </div>
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('admin.create_password')}</span>
              {/* autoComplete="new-password" so the administrator's own manager
                  does not offer, or silently save, THEIR credential here. */}
              <PasswordInput className="fx-input" autoComplete="new-password" value={password}
                             onChange={(e) => setPassword(e.target.value)}
                             aria-label={t('admin.create_password')} />
            </label>
            <PasswordStrength value={password} />

            <label className="fx-field">
              <span className="fx-field-label">{t('admin.col_role')}</span>
              <div className="fx-input">
                <select value={role} onChange={(e) => setRole(e.target.value as Role)}
                        aria-label={t('admin.col_role')}
                        style={{ width: '100%', border: 0, background: 'transparent', font: 'inherit', color: 'inherit' }}>
                  {ASSIGNABLE.map((r) => (
                    <option key={r} value={r}>{t(`admin.role_${r}`)}</option>
                  ))}
                </select>
              </div>
            </label>
          </div>

          <div className="fx-drawer-actions">
            <button type="button" className="fx-btn" onClick={onClose}>
              {t('common.cancel')}
            </button>
            <button type="submit" className="fx-btn fx-btn-primary" disabled={blocked}>
              <Icon d={I.plus} size={13} stroke={2.2} /> {t('admin.create_submit')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

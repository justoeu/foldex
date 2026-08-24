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
import { generatePassword, GENERATED_MAX_LENGTH } from '../../lib/generatePassword'
import { useInstancePolicy } from '../../hooks/useInstancePolicy'
import { SecretBand } from '../SecretBand'

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
  const [confirm, setConfirm] = useState('')
  // Whether the value in the field came from `generatePassword` and is still
  // untouched. It decides two things: the plaintext band is shown, and the
  // confirmation field is not asked for — see the render.
  const [generated, setGenerated] = useState(false)
  const [role, setRole] = useState<Role>('editor')
  const [error, setError] = useState('')

  // The floor is owner-configurable (ADR-35), and a generated password that
  // the server always refuses would be a button that cannot work — the same
  // defect as offering the e-mail factor on an instance with no SMTP. Admins
  // may READ the policy, which is what makes this available here.
  const policy = useInstancePolicy()
  const minLen = policy.minPasswordLen

  function applyGenerated(value: string) {
    setPassword(value)
    // Cleared, or editing the generated value by hand brings the confirmation
    // back pre-filled with something never typed for THIS password.
    setConfirm('')
    setGenerated(true)
    // A standing `email_taken` is about the address and is still true; wiping
    // it here would clear a refusal whose cause did not change.
  }

  /** Any manual edit revokes the generated status, so confirmation returns. */
  function typePassword(value: string) {
    setPassword(value)
    setGenerated(false)
  }

  const create = useMutation({
    mutationFn: () => admin.createUser({ email: email.trim(), name: name.trim(), password, role }),
    onSuccess: async () => {
      // The page keys on ['admin', 'users'] / ['admin', 'invites']; invalidating the
      // prefix is what the page's own mutations do.
      // Named keys rather than the `['admin']` prefix, which would also
      // invalidate `['admin','policy']` — still observed by this dialog until
      // it unmounts — so closing would wait on a refetch of a document the
      // creation cannot have changed. `roles` IS included: the matrix renders
      // a per-role account count, and a new account changes it.
      await Promise.all([
        qc.invalidateQueries({ queryKey: ['admin', 'users'] }),
        qc.invalidateQueries({ queryKey: ['admin', 'invites'] }),
        qc.invalidateQueries({ queryKey: ['admin', 'roles'] }),
      ])
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
        setError(apiErrorMessage(e) ?? t('auth_errors.password_too_short', { count: minLen }))
      else if (code === 'invalid_role') setError(t('admin.err_invalid_role'))
      else setError(t('auth_errors.generic'))
    },
  })

  // Safe to ask freely HERE and nowhere else: past RequireAdmin the caller can
  // already list every account with its address, so the probe discloses nothing
  // they could not read directly. The e-mail-CHANGE flow deliberately has no
  // such endpoint — there a password is the cost of each guess.
  const avail = useAvailability(admin.emailAvailable, email)
  // A generated value has no typo to catch, so it is not confirmed. Asking
  // anyway would mean transcribing a 20-character random string twice, into
  // the one field where a mismatch is impossible by construction.
  const mismatch = !generated && confirm !== password
  // The client length gate exists to save a pointless round trip, not to be
  // the authority. It is capped at what a password can actually BE — bcrypt
  // truncates at 72 bytes — because `MaxPasswordFloor` is 128: on an instance
  // configured above 72, gating on the raw floor makes both a typed and a
  // generated value un-submittable, so the administrator gets a dead button
  // instead of the server's refusal, which is the only thing that names the
  // real number and reveals the misconfiguration.
  const gateLen = Math.min(minLen, GENERATED_MAX_LENGTH)
  const blocked =
    !email.trim() ||
    password.length < gateLen ||
    mismatch ||
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
              {/* autoComplete="new-password" stops the administrator's own
                  manager from AUTOFILLING their credential here. It does not
                  stop the browser's save prompt — this is the exact field
                  Chrome and 1Password target — so the admin's vault may keep
                  the target user's temporary password. Documented in the
                  README rather than papered over: it is the same window
                  INV-021 already declares. */}
              <PasswordInput className="fx-input" autoComplete="new-password" value={password}
                             onChange={(e) => typePassword(e.target.value)}
                             aria-label={t('admin.create_password')} />
              {/* The floor is owner-configurable, so the gate above can refuse
                  a password for a reason no constant on this screen knows. */}
              <span className="fx-sec-row-hint">{t('auth.password_min', { count: minLen })}</span>
            </label>
            {/* Guidance for a person INVENTING a password. Over 116 bits of
                randomness it only reports a missing symbol class, which reads
                as the product marking its own output deficient. */}
            {!generated && <PasswordStrength value={password} />}

            <div className="fx-sec-actions">
              {/* Disabled only while the floor is still unknown: generating a
                  20-character value against an instance that demands 24 would
                  produce a password the server refuses. A FAILED policy query
                  falls back to the compiled-in floor and still generates. */}
              <button type="button" className="fx-btn" disabled={policy.isPending}
                      onClick={() => applyGenerated(generatePassword(minLen))}>
                <Icon d={I.sparkles} size={13} /> {t('admin.create_generate')}
              </button>
            </div>

            {generated ? (
              /* The generated value in clear, because a password nobody can
                 read is a password nobody can hand over. It gets a band of its
                 own rather than relying on the field's reveal toggle: this is
                 the thing to copy before the drawer closes, and the toggle
                 deliberately forgets its state on every remount. */
              <SecretBand
                label={t('admin.create_generated_label')}
                value={password}
                testId="generated-password"
                hint={<span className="fx-sec-row-hint">{t('admin.create_generated_hint')}</span>}
              />
            ) : (
              <label className="fx-field">
                <span className="fx-field-label">{t('admin.create_confirm')}</span>
                <PasswordInput className="fx-input" autoComplete="new-password" value={confirm}
                               onChange={(e) => setConfirm(e.target.value)}
                               aria-label={t('admin.create_confirm')} />
                {/* Polite, not an alert: it appears while the administrator is
                    still typing the second field, and an assertive region
                    would interrupt them on the keystroke before the match. */}
                {confirm.length > 0 && mismatch && (
                  <span className="fx-inline-error" role="status">
                    <Icon d={I.alert} size={13} /> {t('auth_errors.password_mismatch')}
                  </span>
                )}
              </label>
            )}

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

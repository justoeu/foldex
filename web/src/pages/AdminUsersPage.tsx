import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from '../components/icons'
import { CreateUserDialog } from '../components/admin/CreateUserDialog'
import { InvitePanel } from '../components/admin/InvitePanel'
import { UserRowActions } from '../components/admin/UserRowActions'
import {
  deleteUser,
  listUsers,
  revokeUserSessions,
  sendPasswordRecovery,
  transferOwnership,
  updateUser,
} from '../api/admin'
import { apiErrorCode as errCode } from '../lib/apiError'
import { useAuth } from '../auth/AuthProvider'
import { ASSIGNABLE_ROLES, hasSecondFactor, type AuthUser, type Role } from '../auth/types'
import { ROLE_INITIALS, ROLE_TONE } from '../components/admin/RolesMatrix'
import { relativeTime } from '../components/admin/AdminOverview'
import { canMutateAccount, countActiveAdmins } from '../components/admin/canMutateAccount'

function statusTone(status: AuthUser['status']): string {
  if (status === 'active') return 'fx-chip-ok'
  if (status === 'disabled') return 'fx-chip-danger'
  return 'fx-chip-warn'
}

/**
 * The administrator's view of every account on the instance.
 *
 * It shows accounts, never their CONTENT. Segmentation is absolute: an
 * administrator can disable, promote or delete a user, and can send recovery
 * to their verified mailbox — but cannot read another user's links or notes.
 * The page is deliberately built so that nothing here suggests otherwise.
 *
 * Every disabled button below mirrors a rule the SERVER enforces inside a
 * transaction (you cannot demote, disable or delete yourself; the last active
 * admin cannot be removed by anyone). The mirroring exists so a user never
 * reaches a dead end the UI implied was open — not as the guard itself.
 */
export function AdminUsersPage() {
  const [createOpen, setCreateOpen] = useState(false)
  const { t } = useTranslation()
  const qc = useQueryClient()
  const { session } = useAuth()
  const me = session.status === 'authenticated' ? session.user : null

  const users = useQuery({ queryKey: ['admin', 'users'], queryFn: listUsers })

  const [error, setError] = useState('')
  const [recoverySent, setRecoverySent] = useState('')

  function refresh() {
    return qc.invalidateQueries({ queryKey: ['admin'] })
  }
  function onError(err: unknown) {
    setError(messageFor(err, t))
  }

  const patch = useMutation({
    mutationFn: (v: { id: number; role?: Role; status?: 'active' | 'disabled' }) =>
      updateUser(v.id, { role: v.role, status: v.status }),
    onSuccess: () => {
      setError('')
      return refresh()
    },
    onError,
  })

  const remove = useMutation({
    mutationFn: deleteUser,
    onSuccess: () => {
      setError('')
      return refresh()
    },
    onError,
  })

  const revokeSessions = useMutation({ mutationFn: revokeUserSessions, onError })

  const resetPassword = useMutation({
    mutationFn: (u: AuthUser) => sendPasswordRecovery(u.id).then(() => u.email),
    onSuccess: (email) => {
      setError('')
      setRecoverySent(email)
      return refresh()
    },
    onError,
  })

  // Transferring revokes EVERY session of both accounts, including the caller's
  // own — so there is nothing to refresh afterwards. The next request lands as
  // anonymous and the app's refresh interceptor routes to the login screen,
  // which is the honest outcome: the caller is no longer the owner.
  const transfer = useMutation({
    mutationFn: transferOwnership,
    onSuccess: () => refresh(),
    onError,
  })

  const activeAdmins = countActiveAdmins(users.data ?? [])
  const busy = {
    patch: patch.isPending,
    revoke: revokeSessions.isPending,
    reset: resetPassword.isPending,
    transfer: transfer.isPending,
    remove: remove.isPending,
  }

  return (
    <div>
      {error && (
        <div className="fx-inline-error" role="alert" style={{ fontSize: 12, marginBottom: 12 }}>
          {error}
        </div>
      )}

      {recoverySent && (
        <div className="fx-card" role="status" style={{ marginBottom: 16 }}>
          <div className="fx-card-body" style={{ gap: 8, padding: 18 }}>
            <strong style={{ fontSize: 13 }}>{t('admin.recovery_sent', { email: recoverySent })}</strong>
            <p style={{ fontSize: 11, color: 'var(--fx-ink-3)', margin: 0 }}>
              {t('admin.recovery_sent_detail')}
            </p>
          </div>
        </div>
      )}

      <section className="fx-card" style={{ marginBottom: 16 }}>
        <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, flexWrap: 'wrap' }}>
            <h3
              className="fx-card-title"
              style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}
            >
              <Icon d={I.users} size={15} /> {t('admin.users_title')}
            </h3>
            <button className="fx-btn fx-btn-primary" onClick={() => setCreateOpen(true)}>
              <Icon d={I.plus} size={13} stroke={2.2} /> {t('admin.create_submit')}
            </button>
          </div>

          <div className="fx-utable-wrap">
            <table className="fx-utable">
              <thead>
                <tr>
                  <th>{t('admin.col_user')}</th>
                  <th>{t('admin.col_role')}</th>
                  <th>{t('admin.col_last_seen')}</th>
                  <th>{t('admin.col_status')}</th>
                  <th aria-label={t('admin.col_actions')} />
                </tr>
              </thead>
              <tbody>
                {(users.data ?? []).map((u) => {
                  const can = canMutateAccount(u, me, activeAdmins)
                  const isOwner = u.role === 'owner'
                  return (
                    <tr key={u.id}>
                      <td>
                        <div className="fx-utable-user">
                          <span className={'fx-rolebadge ' + ROLE_TONE[u.role]}>
                            {ROLE_INITIALS[u.role]}
                          </span>
                          <div style={{ minWidth: 0 }}>
                            <div className="fx-utable-name">{u.name || u.email}</div>
                            <div className="fx-utable-mail">{u.email}</div>
                          </div>
                        </div>
                      </td>
                      <td>
                        <select
                          className="fx-input"
                          style={{ width: 'auto' }}
                          aria-label={t('admin.role_label', { email: u.email })}
                          value={u.role}
                          disabled={!can.role || patch.isPending}
                          onChange={(e) => patch.mutate({ id: u.id, role: e.target.value as Role })}
                        >
                          {/* Owner appears only when the row already holds it,
                              and never as something to pick: the server refuses
                              an assignment to owner, so offering it would produce
                              a request that always fails. */}
                          {isOwner && <option value="owner">{t('admin.role_owner')}</option>}
                          {ASSIGNABLE_ROLES.map((r) => (
                            <option value={r} key={r}>{t(`admin.role_${r}`)}</option>
                          ))}
                        </select>
                      </td>
                      <td className="fx-utable-meta">
                        {u.last_login_at ? relativeTime(u.last_login_at) : t('admin.never_signed_in')}
                      </td>
                      <td>
                        <span className={'fx-chip ' + statusTone(u.status)}>
                          {t(`admin.status_${u.status}`)}
                        </span>
                        {hasSecondFactor(u) && (
                          <span className="fx-chip fx-chip-ok" style={{ marginLeft: 4 }}>
                            {t('admin.has_2fa')}
                          </span>
                        )}
                        {!u.has_password && (
                          <span className="fx-chip" style={{ marginLeft: 4 }}>
                            {t('admin.google_only')}
                          </span>
                        )}
                      </td>
                      <td>
                        <UserRowActions
                          u={u}
                          me={me}
                          activeAdmins={activeAdmins}
                          busy={busy}
                          onToggleStatus={(row) =>
                            patch.mutate({
                              id: row.id,
                              status: row.status === 'active' ? 'disabled' : 'active',
                            })
                          }
                          onRevokeSessions={(id) => revokeSessions.mutate(id)}
                          onReset={(row) => resetPassword.mutate(row)}
                          onTransfer={(id) => transfer.mutate(id)}
                          onDelete={(id) => remove.mutate(id)}
                        />
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <InvitePanel onError={onError} onClearError={() => setError('')} />

      {createOpen && <CreateUserDialog onClose={() => setCreateOpen(false)} />}
    </div>
  )
}

function messageFor(err: unknown, t: (k: string, o?: Record<string, unknown>) => string): string {
  switch (errCode(err)) {
    case 'last_admin':
      return t('admin.err_last_admin')
    case 'self_target':
      return t('admin.err_self_target')
    case 'email_taken':
      return t('auth_errors.email_taken')
    case 'invalid_email':
      return t('auth_errors.invalid_email')
    case 'smtp_required':
      return t('admin.err_smtp_required')
    case 'mail_unavailable':
      return t('admin.err_mail_unavailable')
    case 'recovery_unavailable':
      return t('admin.err_recovery_unavailable')
    default:
      return t('auth_errors.generic')
  }
}

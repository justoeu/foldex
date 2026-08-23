import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from '../components/icons'
import { CreateUserDialog } from '../components/admin/CreateUserDialog'
import { useConfirm } from '../components/ConfirmDialog'
import {
  createInvite,
  deleteUser,
  listInvites,
  listUsers,
  revokeInvite,
  revokeUserSessions,
  sendPasswordRecovery,
  transferOwnership,
  updateUser,
  type Invite,
} from '../api/admin'
import { apiErrorCode as errCode } from '../lib/apiError'
import { useAuth } from '../auth/AuthProvider'
import { ASSIGNABLE_ROLES, isAdminRole, type AuthUser, type Role } from '../auth/types'
import { ROLE_INITIALS, ROLE_TONE } from '../components/admin/RolesMatrix'
import { relativeTime } from '../components/admin/AdminOverview'

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
  const confirmAction = useConfirm()
  const { session } = useAuth()
  const me = session.status === 'authenticated' ? session.user : null

  const users = useQuery({ queryKey: ['admin', 'users'], queryFn: listUsers })
  const invites = useQuery({ queryKey: ['admin', 'invites'], queryFn: listInvites })

  const [error, setError] = useState('')
  const [recoverySent, setRecoverySent] = useState('')
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteRole, setInviteRole] = useState<Role>('editor')
  const [lastInvite, setLastInvite] = useState<Invite | null>(null)

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

  const invite = useMutation({
    mutationFn: () => createInvite(inviteEmail.trim(), inviteRole),
    onSuccess: (inv) => {
      setError('')
      setLastInvite(inv)
      setInviteEmail('')
      return refresh()
    },
    onError,
  })

  const dropInvite = useMutation({
    mutationFn: revokeInvite,
    onSuccess: () => refresh(),
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

  // Counts every role that can administer, mirroring guardLastAdminTx: with
  // four roles, counting only 'admin' would call an instance whose sole
  // administrator is the owner "down to zero" and disable buttons that work.
  const activeAdmins = (users.data ?? []).filter(
    (u) => isAdminRole(u.role) && u.status === 'active',
  ).length

  async function askDelete(u: AuthUser) {
    const ok = await confirmAction({
      title: t('admin.delete_title'),
      message: t('admin.delete_message', { email: u.email }),
      destructive: true,
    })
    if (ok) remove.mutate(u.id)
  }

  async function askTransfer(u: AuthUser) {
    const ok = await confirmAction({
      title: t('admin.transfer_title'),
      message: t('admin.transfer_message', { email: u.email }),
      destructive: true,
    })
    if (ok) transfer.mutate(u.id)
  }

  async function askReset(u: AuthUser) {
    const ok = await confirmAction({
      title: t('admin.reset_title'),
      message: t('admin.reset_message', { email: u.email }),
      destructive: true,
    })
    if (ok) resetPassword.mutate(u)
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
                  const isSelf = me?.id === u.id
                  const isLastAdmin = isAdminRole(u.role) && u.status === 'active' && activeAdmins <= 1
                  // The owner is out of reach of every ordinary edit — the server
                  // refuses role and status changes on that row outright, and the
                  // seat moves only through transfer.
                  const isOwner = u.role === 'owner'
                  // One rule, three buttons: demote, disable and delete all end in
                  // "this account stops being an active admin", and each is blocked
                  // for the same reasons.
                  const locked = isSelf || isLastAdmin || isOwner
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
                          disabled={locked || patch.isPending}
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
                        {u.totp_enabled && (
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
                        {/* Icon-only, and therefore LABELLED TWICE: `aria-label`
                            names the action for a screen reader and `data-tooltip`
                            shows the same words on hover/focus for everyone else.
                            A row of five unlabelled glyphs — two of which disable
                            an account and delete it — would be a guessing game,
                            and the two destructive ones keep their confirmation
                            dialog regardless. Every label already carries the
                            e-mail, so two rows never read the same. */}
                        <div className="fx-utable-actions">
                          <button
                            className="fx-rowact"
                            aria-label={
                              u.status === 'active'
                                ? t('admin.disable_label', { email: u.email })
                                : t('admin.enable_label', { email: u.email })
                            }
                            data-tooltip={
                              u.status === 'active' ? t('admin.disable') : t('admin.enable')
                            }
                            disabled={locked || patch.isPending}
                            onClick={() =>
                              patch.mutate({
                                id: u.id,
                                status: u.status === 'active' ? 'disabled' : 'active',
                              })
                            }
                          >
                            <Icon d={u.status === 'active' ? I.userOff : I.userCheck} size={14} />
                            {u.status === 'active' ? t('admin.disable') : t('admin.enable')}
                          </button>

                          <button
                            className="fx-rowact"
                            aria-label={t('admin.revoke_sessions_label', { email: u.email })}
                            data-tooltip={t('admin.revoke_sessions')}
                            disabled={revokeSessions.isPending}
                            onClick={() => revokeSessions.mutate(u.id)}
                          >
                            <Icon d={I.logout} size={14} />
                            {t('admin.revoke_sessions')}
                          </button>

                          <button
                            className="fx-rowact"
                            aria-label={t('admin.force_reset_label', { email: u.email })}
                            data-tooltip={t('admin.force_reset')}
                            disabled={isSelf || resetPassword.isPending}
                            onClick={() => void askReset(u)}
                          >
                            <Icon d={I.key} size={14} />
                            {t('admin.force_reset')}
                          </button>

                          {/* Transferring is offered only by the owner, and only
                              onto an active account — the two conditions the
                              server checks before it moves the seat. */}
                          {me?.role === 'owner' && !isSelf && u.status === 'active' && (
                            <button
                              className="fx-rowact"
                              aria-label={t('admin.transfer_label', { email: u.email })}
                              data-tooltip={t('admin.transfer')}
                              disabled={transfer.isPending}
                              onClick={() => void askTransfer(u)}
                            >
                              <Icon d={I.crown} size={14} />
                              {t('admin.transfer')}
                            </button>
                          )}

                          <button
                            className="fx-rowact fx-rowact-danger"
                            aria-label={t('admin.delete_label', { email: u.email })}
                            data-tooltip={t('admin.delete')}
                            disabled={locked || remove.isPending}
                            onClick={() => void askDelete(u)}
                          >
                            <Icon d={I.trash} size={14} />
                            {t('admin.delete')}
                          </button>
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <section className="fx-card">
        <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
          <h3
            className="fx-card-title"
            style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}
          >
            <Icon d={I.plus} size={15} /> {t('admin.invites_title')}
          </h3>
          <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>
            {t('admin.invites_desc')}
          </p>

          {/* The accept URL is shown because with the default `log` mail driver
              there is no inbox to check — an admin who cannot copy the link has
              no way to invite anybody. It is also the only time the raw token
              exists: the database keeps sha256. */}
          {lastInvite?.accept_url && (
            <div style={{ display: 'grid', gap: 6 }}>
              <strong style={{ fontSize: 12 }}>{t('admin.invite_link_title')}</strong>
              <code
                data-testid="invite-link"
                style={{ fontSize: 11, wordBreak: 'break-all', fontFamily: 'var(--fx-mono)' }}
              >
                {lastInvite.accept_url}
              </code>
            </div>
          )}

          <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'grid', gap: 8 }}>
            {(invites.data ?? []).map((inv) => (
              <li key={inv.id} style={{ display: 'flex', gap: 10, alignItems: 'center', fontSize: 12 }}>
                <span style={{ flex: 1 }}>
                  <strong>{inv.email}</strong>
                  <span style={{ color: 'var(--fx-ink-4)' }}> · {t(`admin.role_${inv.role}`)}</span>
                </span>
                <button
                  className="fx-btn"
                  aria-label={t('admin.revoke_invite_label', { email: inv.email })}
                  onClick={() => dropInvite.mutate(inv.id)}
                >
                  <Icon d={I.trash} size={13} />
                </button>
              </li>
            ))}
            {invites.data?.length === 0 && (
              <li style={{ fontSize: 12, color: 'var(--fx-ink-4)' }}>{t('admin.no_invites')}</li>
            )}
          </ul>

          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'flex-end' }}>
            <label className="fx-field" style={{ margin: 0, flex: 1, minWidth: 200 }}>
              <span className="fx-field-label">{t('admin.invite_email')}</span>
              <input
                className="fx-input"
                type="email"
                value={inviteEmail}
                onChange={(e) => setInviteEmail(e.target.value)}
              />
            </label>
            <label className="fx-field">
              <span className="fx-field-label">{t('admin.invite_role')}</span>
              <select
                className="fx-input"
                value={inviteRole}
                onChange={(e) => setInviteRole(e.target.value as Role)}
              >
                {/* Same source as the row editor above: an invitation can mint
                    an administrator but never an owner, which is exactly what
                    ASSIGNABLE_ROLES encodes. */}
                {ASSIGNABLE_ROLES.map((r) => (
                  <option value={r} key={r}>{t(`admin.role_${r}`)}</option>
                ))}
              </select>
            </label>
            <button
              className="fx-btn fx-btn-primary"
              disabled={invite.isPending || !inviteEmail.trim()}
              onClick={() => invite.mutate()}
            >
              {t('admin.send_invite')}
            </button>
          </div>
        </div>
      </section>

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

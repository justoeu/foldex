import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from '../components/icons'
import { useConfirm } from '../components/ConfirmDialog'
import {
  createInvite,
  deleteUser,
  forcePasswordReset,
  listInvites,
  listUsers,
  revokeInvite,
  revokeUserSessions,
  updateUser,
  type Invite,
} from '../api/admin'
import { apiErrorCode as errCode } from '../lib/apiError'
import { useAuth } from '../auth/AuthProvider'
import type { AuthUser, Role } from '../auth/types'

/**
 * The administrator's view of every account on the instance.
 *
 * It shows accounts, never their CONTENT. Segmentation is absolute: an
 * administrator can disable, promote or delete a user, and can hand back a
 * password — but cannot read a single link or note belonging to anybody else.
 * The page is deliberately built so that nothing here suggests otherwise.
 *
 * Every disabled button below mirrors a rule the SERVER enforces inside a
 * transaction (you cannot demote, disable or delete yourself; the last active
 * admin cannot be removed by anyone). The mirroring exists so a user never
 * reaches a dead end the UI implied was open — not as the guard itself.
 */
export function AdminUsersPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const confirmAction = useConfirm()
  const { session } = useAuth()
  const me = session.status === 'authenticated' ? session.user : null

  const users = useQuery({ queryKey: ['admin', 'users'], queryFn: listUsers })
  const invites = useQuery({ queryKey: ['admin', 'invites'], queryFn: listInvites })

  const [error, setError] = useState('')
  const [tempPassword, setTempPassword] = useState<{ email: string; password: string } | null>(null)
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteRole, setInviteRole] = useState<Role>('user')
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
    mutationFn: (u: AuthUser) => forcePasswordReset(u.id).then((p) => ({ email: u.email, password: p })),
    onSuccess: (v) => {
      setError('')
      setTempPassword(v)
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

  const activeAdmins = (users.data ?? []).filter(
    (u) => u.role === 'admin' && u.status === 'active',
  ).length

  async function askDelete(u: AuthUser) {
    const ok = await confirmAction({
      title: t('admin.delete_title'),
      message: t('admin.delete_message', { email: u.email }),
      destructive: true,
    })
    if (ok) remove.mutate(u.id)
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
    <div style={{ padding: 6, maxWidth: 860 }}>
      <div className="fx-pagehead" style={{ marginBottom: 18 }}>
        <div>
          <div className="fx-pagehead-kicker">{t('admin.page_kicker')}</div>
          <h1 className="fx-pagehead-h">{t('admin.page_title')}</h1>
        </div>
      </div>

      {error && (
        <div className="fx-inline-error" role="alert" style={{ fontSize: 12, marginBottom: 12 }}>
          {error}
        </div>
      )}

      {/* Shown once, and never stored. The admin reads it out to the user; it
          is deliberately NOT e-mailed, because the mailbox may be exactly what
          the account lost access to. */}
      {tempPassword && (
        <div className="fx-card" style={{ marginBottom: 16 }}>
          <div className="fx-card-body" style={{ gap: 8, padding: 18 }}>
            <strong style={{ fontSize: 13 }}>
              {t('admin.temp_password_title', { email: tempPassword.email })}
            </strong>
            <code
              data-testid="temp-password"
              style={{ fontSize: 15, fontFamily: 'var(--fx-mono)', letterSpacing: 1 }}
            >
              {tempPassword.password}
            </code>
            <p style={{ fontSize: 11, color: 'var(--fx-ink-3)', margin: 0 }}>
              {t('admin.temp_password_warning')}
            </p>
            <div>
              <button className="fx-btn" onClick={() => setTempPassword(null)}>
                {t('common.close')}
              </button>
            </div>
          </div>
        </div>
      )}

      <section className="fx-card" style={{ marginBottom: 16 }}>
        <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
          <h3
            className="fx-card-title"
            style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}
          >
            <Icon d={I.users} size={15} /> {t('admin.users_title')}
          </h3>

          <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'grid', gap: 10 }}>
            {(users.data ?? []).map((u) => {
              const isSelf = me?.id === u.id
              const isLastAdmin = u.role === 'admin' && u.status === 'active' && activeAdmins <= 1
              // One rule, three buttons: demote, disable and delete all end in
              // "this account stops being an active admin", and each is blocked
              // for the same two reasons.
              const locked = isSelf || isLastAdmin
              return (
                <li
                  key={u.id}
                  style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap', fontSize: 12 }}
                >
                  <span style={{ flex: 1, minWidth: 200 }}>
                    <strong>{u.email}</strong>
                    {u.name ? <span style={{ color: 'var(--fx-ink-4)' }}> · {u.name}</span> : null}
                    <span style={{ color: 'var(--fx-ink-4)' }}>
                      {' '}
                      · {t(`admin.role_${u.role}`)} · {t(`admin.status_${u.status}`)}
                      {u.totp_enabled ? ` · ${t('admin.has_2fa')}` : ''}
                      {u.has_password ? '' : ` · ${t('admin.google_only')}`}
                    </span>
                  </span>

                  <select
                    className="fx-input"
                    style={{ width: 'auto' }}
                    aria-label={t('admin.role_label', { email: u.email })}
                    value={u.role}
                    disabled={locked || patch.isPending}
                    onChange={(e) => patch.mutate({ id: u.id, role: e.target.value as Role })}
                  >
                    <option value="user">{t('admin.role_user')}</option>
                    <option value="admin">{t('admin.role_admin')}</option>
                  </select>

                  <button
                    className="fx-btn"
                    disabled={locked || patch.isPending}
                    onClick={() =>
                      patch.mutate({
                        id: u.id,
                        status: u.status === 'active' ? 'disabled' : 'active',
                      })
                    }
                  >
                    {u.status === 'active' ? t('admin.disable') : t('admin.enable')}
                  </button>

                  <button
                    className="fx-btn"
                    disabled={revokeSessions.isPending}
                    onClick={() => revokeSessions.mutate(u.id)}
                  >
                    {t('admin.revoke_sessions')}
                  </button>

                  <button
                    className="fx-btn"
                    disabled={isSelf || resetPassword.isPending}
                    onClick={() => void askReset(u)}
                  >
                    {t('admin.force_reset')}
                  </button>

                  <button
                    className="fx-btn"
                    aria-label={t('admin.delete_label', { email: u.email })}
                    disabled={locked || remove.isPending}
                    onClick={() => void askDelete(u)}
                  >
                    <Icon d={I.trash} size={13} />
                  </button>
                </li>
              )
            })}
          </ul>
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
            <label className="fx-field" style={{ margin: 0 }}>
              <span className="fx-field-label">{t('admin.invite_role')}</span>
              <select
                className="fx-input"
                value={inviteRole}
                onChange={(e) => setInviteRole(e.target.value as Role)}
              >
                <option value="user">{t('admin.role_user')}</option>
                <option value="admin">{t('admin.role_admin')}</option>
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
    default:
      return t('auth_errors.generic')
  }
}

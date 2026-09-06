import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from '../icons'
import { createInvite, listInvites, revokeInvite, type Invite } from '../../api/admin'
import { ASSIGNABLE_ROLES, type Role } from '../../auth/types'

type Props = {
  onError: (err: unknown) => void
  onClearError: () => void
}

/**
 * Invitations live next to the account table, not inside it: minting a user
 * is a different act from editing one. The accept URL is shown because the
 * default `log` mail driver has no inbox — without the link there is no way
 * to invite anybody. The raw token exists only here; the database keeps sha256.
 */
export function InvitePanel({ onError, onClearError }: Props) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const invites = useQuery({ queryKey: ['admin', 'invites'], queryFn: listInvites })
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteRole, setInviteRole] = useState<Role>('editor')
  const [lastInvite, setLastInvite] = useState<Invite | null>(null)

  const invite = useMutation({
    mutationFn: () => createInvite(inviteEmail.trim(), inviteRole),
    onSuccess: (inv) => {
      onClearError()
      setLastInvite(inv)
      setInviteEmail('')
      return qc.invalidateQueries({ queryKey: ['admin'] })
    },
    onError,
  })

  const dropInvite = useMutation({
    mutationFn: revokeInvite,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin'] }),
    onError,
  })

  return (
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
  )
}

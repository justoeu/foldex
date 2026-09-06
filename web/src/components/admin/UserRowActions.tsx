import { useTranslation } from 'react-i18next'
import { Icon, I } from '../icons'
import { useConfirm } from '../ConfirmDialog'
import type { AuthUser } from '../../auth/types'
import { canMutateAccount } from './canMutateAccount'

type Busy = {
  patch: boolean
  revoke: boolean
  reset: boolean
  transfer: boolean
  remove: boolean
}

type Props = {
  u: AuthUser
  me: AuthUser | null
  activeAdmins: number
  busy: Busy
  onToggleStatus: (u: AuthUser) => void
  onRevokeSessions: (id: number) => void
  onReset: (u: AuthUser) => void
  onTransfer: (id: number) => void
  onDelete: (id: number) => void
}

/**
 * The five row actions. Disable/delete/role/transfer come from the lock
 * matrix; recovery is blocked only on self. Confirmation for the destructive
 * ones lives here so the page does not re-derive which buttons exist.
 */
export function UserRowActions({
  u, me, activeAdmins, busy,
  onToggleStatus, onRevokeSessions, onReset, onTransfer, onDelete,
}: Props) {
  const { t } = useTranslation()
  const confirmAction = useConfirm()
  const can = canMutateAccount(u, me, activeAdmins)
  const isSelf = me?.id === u.id

  async function askDelete() {
    const ok = await confirmAction({
      title: t('admin.delete_title'),
      message: t('admin.delete_message', { email: u.email }),
      destructive: true,
    })
    if (ok) onDelete(u.id)
  }

  async function askTransfer() {
    const ok = await confirmAction({
      title: t('admin.transfer_title'),
      message: t('admin.transfer_message', { email: u.email }),
      destructive: true,
    })
    if (ok) onTransfer(u.id)
  }

  async function askReset() {
    const ok = await confirmAction({
      title: t('admin.reset_title'),
      message: t('admin.reset_message', { email: u.email }),
      destructive: true,
    })
    if (ok) onReset(u)
  }

  return (
    <div className="fx-utable-actions">
      <button
        className="fx-rowact"
        aria-label={
          u.status === 'active'
            ? t('admin.disable_label', { email: u.email })
            : t('admin.enable_label', { email: u.email })
        }
        data-tooltip={u.status === 'active' ? t('admin.disable') : t('admin.enable')}
        disabled={!can.disable || busy.patch}
        onClick={() => onToggleStatus(u)}
      >
        <Icon d={u.status === 'active' ? I.userOff : I.userCheck} size={14} />
        {u.status === 'active' ? t('admin.disable') : t('admin.enable')}
      </button>

      <button
        className="fx-rowact"
        aria-label={t('admin.revoke_sessions_label', { email: u.email })}
        data-tooltip={t('admin.revoke_sessions')}
        disabled={busy.revoke}
        onClick={() => onRevokeSessions(u.id)}
      >
        <Icon d={I.logout} size={14} />
        {t('admin.revoke_sessions')}
      </button>

      <button
        className="fx-rowact"
        aria-label={t('admin.force_reset_label', { email: u.email })}
        data-tooltip={t('admin.force_reset')}
        disabled={isSelf || busy.reset}
        onClick={() => void askReset()}
      >
        <Icon d={I.key} size={14} />
        {t('admin.force_reset')}
      </button>

      {can.transfer && (
        <button
          className="fx-rowact"
          aria-label={t('admin.transfer_label', { email: u.email })}
          data-tooltip={t('admin.transfer')}
          disabled={busy.transfer}
          onClick={() => void askTransfer()}
        >
          <Icon d={I.crown} size={14} />
          {t('admin.transfer')}
        </button>
      )}

      <button
        className="fx-rowact fx-rowact-danger"
        aria-label={t('admin.delete_label', { email: u.email })}
        data-tooltip={t('admin.delete')}
        disabled={!can.delete || busy.remove}
        onClick={() => void askDelete()}
      >
        <Icon d={I.trash} size={14} />
        {t('admin.delete')}
      </button>
    </div>
  )
}

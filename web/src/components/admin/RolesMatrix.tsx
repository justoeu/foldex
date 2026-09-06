import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Icon, I } from '../icons'
import { fetchRoles } from '../../api/admin'
import type { Role } from '../../auth/types'
import { CELL_BLOCK_I18N, orderPermissionGroups } from './matrixPolicy'
import { useRoleMatrixSave } from './useRoleMatrixSave'

/**
 * Which tone each role wears, everywhere it appears on the screen.
 *
 * Exported because the user table paints its role chips from the same map — a
 * role that were amber in one panel and green in the next would read as two
 * different things.
 */
export const ROLE_TONE: Record<Role, string> = {
  owner: 'fx-tone-accent',
  admin: 'fx-tone-pink',
  editor: 'fx-tone-green',
  viewer: 'fx-tone-blue',
}

/** Two-letter badge, matching the mockup's OW / AD / ED / VI. */
export const ROLE_INITIALS: Record<Role, string> = {
  owner: 'OW',
  admin: 'AD',
  editor: 'ED',
  viewer: 'VI',
}

/**
 * The RBAC matrix — read as a grid, and edited in place (ADR-42).
 *
 * Cell policy (locked / editable / escalation / revoke) lives in `matrixPolicy`.
 * Sequential PUTs live in `useRoleMatrixSave`. This file is the grid.
 */
export function RolesMatrix() {
  const { t } = useTranslation()
  const roles = useQuery({ queryKey: ['admin', 'roles'], queryFn: fetchRoles })
  const data = roles.data
  const rows = data?.roles
  const editor = useRoleMatrixSave(data)

  const ordered = useMemo(
    () => orderPermissionGroups(data?.permissions ?? []),
    [data?.permissions],
  )

  if (roles.isPending) return <div className="fx-empty">{t('common.loading')}</div>
  // Guarded on the ARRAY, not merely on `data`: a response whose shape is not
  // what this screen expects reaches here as a truthy object, and indexing into
  // it would crash the whole settings hub over one malformed payload.
  if (roles.isError || !Array.isArray(rows) || !data || !editor.effective) {
    return <div className="fx-empty">{t('admin.roles_unavailable')}</div>
  }

  const { effective, dirty, error, save, toggle, cancel, blockReason } = editor

  return (
    <div className="fx-matrix-wrap">
      {data.editable_disabled && <div className="fx-matrix-note">{t('admin.matrix_compiled')}</div>}
      {!data.editable_disabled && !data.can_edit && (
        <div className="fx-matrix-note">{t('admin.matrix_readonly')}</div>
      )}
      {error && (
        <div className="fx-inline-error" role="alert">
          <Icon d={I.alert} size={13} /> {error}
        </div>
      )}

      <div className="fx-matrix-scroll">
        <table className="fx-matrix">
          <caption className="fx-visually-hidden">{t('admin.matrix_caption')}</caption>
          <thead>
            <tr>
              <th scope="col" className="fx-matrix-corner">{t('admin.matrix_permission')}</th>
              {rows.map((r) => (
                <th scope="col" key={r.role} className="fx-matrix-rolehead">
                  <span className={'fx-rolebadge ' + (ROLE_TONE[r.role] ?? '')}>
                    {ROLE_INITIALS[r.role] ?? '??'}
                  </span>
                  <span className="fx-matrix-rolename">{t(`admin.role_${r.role}`)}</span>
                  <span className="fx-matrix-rolecount">
                    {t('admin.role_user_count', { count: r.user_count })}
                  </span>
                  {!r.editable && (
                    <span className="fx-matrix-rolelock">
                      <Icon d={I.lock} size={10} /> {t('admin.matrix_locked')}
                    </span>
                  )}
                </th>
              ))}
            </tr>
          </thead>
          {ordered.map((group) => (
            <tbody key={group.key}>
              <tr>
                <th scope="colgroup" colSpan={rows.length + 1} className="fx-matrix-group">
                  {t(`admin.permgroup_${group.key}`)}
                </th>
              </tr>
              {group.members.map((p) => (
                <tr key={p}>
                  <th scope="row" className="fx-matrix-perm">
                    <code className="fx-permchip">{p}</code>
                    <span className="fx-matrix-permdesc">{t(`admin.perm_${p.split('.').join('_')}`)}</span>
                  </th>
                  {rows.map((r) => {
                    const on = effective[r.role].has(p)
                    const reason = blockReason(r.role, p)
                    const why = reason ? t(CELL_BLOCK_I18N[reason]) : null
                    return (
                      <td key={r.role} className="fx-matrix-cell">
                        <label className="fx-matrix-check" data-tooltip={why ?? undefined}>
                          <input
                            type="checkbox"
                            checked={on}
                            disabled={why !== null || save.isPending}
                            onChange={() => toggle(r.role, p)}
                            aria-label={t('admin.matrix_cell_label', {
                              permission: p,
                              role: t(`admin.role_${r.role}`),
                            })}
                          />
                          <span aria-hidden="true" className={on ? 'fx-matrix-on' : 'fx-matrix-off'}>
                            <Icon d={on ? I.check : I.x} size={13} stroke={2.4} />
                          </span>
                        </label>
                      </td>
                    )
                  })}
                </tr>
              ))}
            </tbody>
          ))}
        </table>
      </div>

      {data.can_edit && (
        <div className="fx-matrix-actions">
          <button
            className="fx-btn fx-btn-primary"
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate(effective)}
          >
            {t('admin.matrix_save')}
          </button>
          <button
            className="fx-btn"
            disabled={!dirty || save.isPending}
            onClick={cancel}
          >
            {t('common.cancel')}
          </button>
          <span className="fx-sec-row-hint">{t('admin.matrix_hint')}</span>
        </div>
      )}
    </div>
  )
}

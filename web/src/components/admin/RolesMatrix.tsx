import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { fetchRoles } from '../../api/admin'
import type { Role } from '../../auth/types'

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
 * The RBAC matrix, rendered from what the SERVER enforces.
 *
 * Both the role list and each row's permissions come from /api/admin/roles
 * rather than from a table maintained here. A matrix that described a grid the
 * server does not implement would be worse than showing nothing: an
 * administrator would plan around capabilities that do not exist.
 */
export function RolesMatrix() {
  const { t } = useTranslation()
  const roles = useQuery({ queryKey: ['admin', 'roles'], queryFn: fetchRoles })

  if (roles.isPending) return <div className="fx-empty">{t('common.loading')}</div>
  // Guarded on the ARRAY, not merely on `data`: a response whose shape is not
  // what this screen expects reaches here as a truthy object, and indexing into
  // it would crash the whole settings hub over one malformed payload.
  const rows = roles.data?.roles
  if (roles.isError || !Array.isArray(rows)) {
    return <div className="fx-empty">{t('admin.roles_unavailable')}</div>
  }

  return (
    <div>
      {rows.map((r) => (
        <div className="fx-rolerow" key={r.role}>
          <span className={'fx-rolebadge ' + (ROLE_TONE[r.role] ?? '')}>{ROLE_INITIALS[r.role] ?? '??'}</span>
          <div style={{ minWidth: 0, flex: 1 }}>
            <div className="fx-rolerow-name">{t(`admin.role_${r.role}`)}</div>
            <div className="fx-rolerow-scope">{t(`admin.role_${r.role}_scope`)}</div>
            <div className="fx-rolerow-perms">
              {(r.permissions ?? []).map((p) => (
                // The raw permission id, not a translation. These are the
                // server's identifiers and an administrator comparing a role
                // against the API docs needs to see the same string both places.
                <span className="fx-permchip" key={p}>{p}</span>
              ))}
            </div>
          </div>
          <span className="fx-rolerow-count">
            {t('admin.role_user_count', { count: r.user_count })}
          </span>
        </div>
      ))}
    </div>
  )
}

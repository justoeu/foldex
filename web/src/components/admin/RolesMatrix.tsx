import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from '../icons'
import { fetchRoles, setRolePermissions, type RoleSummary, type RolesResponse } from '../../api/admin'
import { apiErrorCode as errCode } from '../../lib/apiError'
import type { Permission, Role } from '../../auth/types'

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
 * Permissions in the groups the server documents them in, so the matrix has
 * bands a reader can navigate instead of fourteen equal rows.
 *
 * A permission missing from every group still renders — under `other` — because
 * a vocabulary the server grew and this list did not would otherwise VANISH
 * from a screen whose whole job is to show what the server enforces.
 */
const GROUPS: { key: string; members: Permission[] }[] = [
  { key: 'content', members: ['content.read', 'content.write'] },
  { key: 'transfer', members: ['backup.export', 'backup.restore', 'import.run'] },
  { key: 'people', members: ['users.read', 'users.write', 'roles.assign', 'invites.read', 'invites.write'] },
  { key: 'instance', members: ['audit.read', 'policy.read', 'policy.write', 'instance.transfer'] },
]

type Draft = Record<Role, Set<Permission>> | null

/**
 * The RBAC matrix — read as a grid, and edited in place (ADR-42).
 *
 * It was four stacked rows of chips, and that shape answers the wrong
 * question. A reader arrives asking "who can restore a backup?" and a chip
 * list makes them scan four paragraphs for one token; the grid makes it one
 * row. The old shape also could not show an ABSENCE — a permission a role
 * lacks simply was not printed, so the screen never distinguished "denied"
 * from "does not exist".
 *
 * Everything about what may be edited comes from the SERVER (`editable`,
 * `locked`, `can_edit`, `caller_role`). Re-deriving any of it here would be
 * two copies of one authorization policy, and the copy that drifts is the one
 * that offers a save the server refuses.
 */
export function RolesMatrix() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const roles = useQuery({ queryKey: ['admin', 'roles'], queryFn: fetchRoles })
  const [draft, setDraft] = useState<Draft>(null)
  const [error, setError] = useState('')

  const data = roles.data
  const rows = data?.roles

  // The vocabulary in group order, with anything the server added and this
  // file does not know about appended rather than dropped.
  const ordered = useMemo<{ key: string; members: Permission[] }[]>(() => {
    const all = data?.permissions ?? []
    const known = new Set(GROUPS.flatMap((g) => g.members))
    const rest = all.filter((p) => !known.has(p))
    const groups = GROUPS.map((g) => ({ key: g.key, members: g.members.filter((p) => all.includes(p)) }))
      .filter((g) => g.members.length > 0)
    return rest.length > 0 ? [...groups, { key: 'other', members: rest }] : groups
  }, [data?.permissions])

  const save = useMutation({
    mutationFn: async (next: Record<Role, Set<Permission>>) => {
      const editable = (rows ?? []).filter((r) => r.editable)
      // One request per changed role, sequentially: the server validates each
      // against the matrix it is about to write from, so sending them in
      // parallel would have two writes racing the same snapshot.
      let last: RoleSummary[] | null = null
      for (const r of editable) {
        const before = new Set(r.permissions)
        const after = next[r.role]
        if (sameSet(before, after)) continue
        const res = await setRolePermissions(r.role, [...after].filter((p) => !lockedSet.has(p)))
        last = res.roles
      }
      return last
    },
    onSuccess: (fresh) => {
      setDraft(null)
      setError('')
      // The last PUT already answered with the whole matrix, resolved after its
      // own write. Refetching would be a fourth request for an answer already
      // in hand — and `user_count` cannot have changed, since a grant edit
      // moves nobody between roles.
      if (fresh) {
        qc.setQueryData(['admin', 'roles'], (prev: RolesResponse | undefined) =>
          prev ? { ...prev, roles: fresh } : prev,
        )
      } else {
        void qc.invalidateQueries({ queryKey: ['admin', 'roles'] })
      }
    },
    onError: (e) => {
      setError(messageFor(errCode(e) ?? '', t))
      // A save spanning several roles is several PUTs, and an earlier one can
      // commit before a later one is refused — so the server's state is now
      // neither the draft nor what the table is showing. Re-read rather than
      // leave the grid asserting rows that are no longer true.
      void qc.invalidateQueries({ queryKey: ['admin', 'roles'] })
      setDraft(null)
    },
  })

  const lockedSet = useMemo(() => new Set(data?.locked ?? []), [data?.locked])

  if (roles.isPending) return <div className="fx-empty">{t('common.loading')}</div>
  // Guarded on the ARRAY, not merely on `data`: a response whose shape is not
  // what this screen expects reaches here as a truthy object, and indexing into
  // it would crash the whole settings hub over one malformed payload.
  if (roles.isError || !Array.isArray(rows) || !data) {
    return <div className="fx-empty">{t('admin.roles_unavailable')}</div>
  }

  const effective = draft ?? snapshot(rows)
  const dirty = draft !== null && rows.some((r) => !sameSet(new Set(r.permissions), effective[r.role]))
  const callerHolds = effective[data.caller_role] ?? new Set<Permission>()

  /** Why a given cell cannot be toggled — or null when it can. */
  function blockedReason(role: Role, p: Permission): string | null {
    if (!data!.can_edit) return t('admin.matrix_why_readonly')
    if (!rows!.find((r) => r.role === role)?.editable) return t('admin.matrix_why_role_locked')
    if (lockedSet.has(p)) return t('admin.matrix_why_permission_locked')
    // Only GRANTING is bounded by what the caller holds; revoking is not, or an
    // admin could never undo a grant an owner made.
    if (!callerHolds.has(p) && !effective[role].has(p)) return t('admin.matrix_why_escalation')
    return null
  }

  function toggle(role: Role, p: Permission) {
    setDraft((cur) => {
      const base = cur ?? snapshot(rows!)
      const next: Record<Role, Set<Permission>> = { ...base }
      const set = new Set(next[role])
      if (set.has(p)) set.delete(p)
      else set.add(p)
      next[role] = set
      return next
    })
  }

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
                    {/* The raw identifier, not a translation: these are the
                        server's strings and an administrator comparing this
                        against the API docs needs to read the same token in
                        both places. The prose sits beside it, not instead. */}
                    <code className="fx-permchip">{p}</code>
                    <span className="fx-matrix-permdesc">{t(`admin.perm_${p.replace('.', '_')}`)}</span>
                  </th>
                  {rows.map((r) => {
                    const on = effective[r.role].has(p)
                    const why = blockedReason(r.role, p)
                    return (
                      <td key={r.role} className="fx-matrix-cell">
                        <label className="fx-matrix-check" data-tooltip={why ?? undefined}>
                          <input
                            type="checkbox"
                            checked={on}
                            disabled={why !== null || save.isPending}
                            onChange={() => toggle(r.role, p)}
                            // Names both axes: "content.write for Editor" is
                            // what a screen reader must say, or every one of
                            // the fifty-six cells announces the same thing.
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
            onClick={() => {
              setDraft(null)
              setError('')
            }}
          >
            {t('common.cancel')}
          </button>
          <span className="fx-sec-row-hint">{t('admin.matrix_hint')}</span>
        </div>
      )}
    </div>
  )
}

/** The server's answer as editable state. */
function snapshot(rows: { role: Role; permissions: Permission[] }[]): Record<Role, Set<Permission>> {
  const out = {} as Record<Role, Set<Permission>>
  for (const r of rows) out[r.role] = new Set(r.permissions)
  return out
}

function sameSet(a: Set<Permission>, b: Set<Permission>) {
  return a.size === b.size && [...a].every((x) => b.has(x))
}

function messageFor(code: string, t: (k: string) => string) {
  switch (code) {
    case 'role_not_editable':
      return t('admin.matrix_why_role_locked')
    case 'permission_locked':
      return t('admin.matrix_why_permission_locked')
    case 'permission_escalation':
      return t('admin.matrix_why_escalation')
    case 'roles_not_configurable':
      return t('admin.matrix_compiled')
    default:
      return t('auth_errors.generic')
  }
}

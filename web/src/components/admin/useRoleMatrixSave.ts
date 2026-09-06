import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { setRolePermissions, type RoleSummary, type RolesResponse } from '../../api/admin'
import { apiErrorCode as errCode } from '../../lib/apiError'
import type { Permission, Role } from '../../auth/types'
import {
  cellBlockReason,
  matrixDirty,
  permissionsForWrite,
  sameSet,
  saveErrorI18n,
  snapshot,
  type CellBlock,
} from './matrixPolicy'

type Draft = Record<Role, Set<Permission>> | null

/**
 * Draft + sequential PUTs for the RBAC matrix.
 *
 * One request per changed role, sequentially: the server validates each
 * against the matrix it is about to write from, so sending them in parallel
 * would have two writes racing the same snapshot.
 */
export function useRoleMatrixSave(data: RolesResponse | undefined) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [draft, setDraft] = useState<Draft>(null)
  const [error, setError] = useState('')
  const rows = data?.roles
  const locked = useMemo(() => new Set(data?.locked ?? []), [data?.locked])

  const save = useMutation({
    mutationFn: async (next: Record<Role, Set<Permission>>) => {
      const editable = (rows ?? []).filter((r) => r.editable)
      let last: RoleSummary[] | null = null
      for (const r of editable) {
        const before = new Set(r.permissions)
        const after = next[r.role]
        if (sameSet(before, after)) continue
        const res = await setRolePermissions(r.role, permissionsForWrite(after, locked))
        last = res.roles
      }
      return last
    },
    onSuccess: (fresh) => {
      setDraft(null)
      setError('')
      if (fresh) {
        qc.setQueryData(['admin', 'roles'], (prev: RolesResponse | undefined) =>
          prev ? { ...prev, roles: fresh } : prev,
        )
      } else {
        void qc.invalidateQueries({ queryKey: ['admin', 'roles'] })
      }
    },
    onError: (e) => {
      setError(t(saveErrorI18n(errCode(e) ?? '')))
      void qc.invalidateQueries({ queryKey: ['admin', 'roles'] })
      setDraft(null)
    },
  })

  const effective = draft ?? (rows ? snapshot(rows) : null)
  const dirty = matrixDirty(rows ?? [], draft)

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

  function cancel() {
    setDraft(null)
    setError('')
  }

  function blockReason(role: Role, p: Permission): CellBlock | null {
    if (!data || !effective) return 'readonly'
    return cellBlockReason({
      canEdit: data.can_edit,
      roleEditable: rows?.find((r) => r.role === role)?.editable === true,
      permissionLocked: locked.has(p),
      callerHolds: (effective[data.caller_role] ?? new Set()).has(p),
      roleHolds: effective[role].has(p),
    })
  }

  return { save, error, effective, dirty, toggle, cancel, blockReason }
}

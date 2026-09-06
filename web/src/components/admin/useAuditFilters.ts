import { useEffect, useMemo, useReducer, useState } from 'react'
import type { AuditCategory, AuditWindow } from '../../api/admin'
import { useRevealTarget } from '../../hooks/useRevealTarget'
import { initialAuditFilters, reduceAuditFilters, toAuditQuery } from './auditFilters'

const SEARCH_DEBOUNCE_MS = 300

/**
 * Window, search, chips, paging and inspect-IP reset for the audit trail.
 *
 * Two queries, one filter. The header aggregates depend only on the WINDOW, so
 * they keep their own key; this hook only builds the list query.
 */
export function useAuditFilters() {
  const [state, dispatch] = useReducer(reduceAuditFilters, undefined, initialAuditFilters)
  const debounced = useDebounced(state.search, SEARCH_DEBOUNCE_MS)
  const filter = useMemo(() => toAuditQuery(state, debounced), [state, debounced])
  const timeline = useRevealTarget<HTMLElement>()

  const inspectIP = (ip: string) => {
    dispatch({ type: 'inspectIP', ip })
    timeline.reveal()
  }

  return {
    period: state.period,
    action: state.action,
    category: state.category,
    search: state.search,
    oldestFirst: state.oldestFirst,
    pages: state.pages,
    filter,
    timeline,
    inspectIP,
    setPeriod: (period: AuditWindow) => dispatch({ type: 'period', period }),
    setSearch: (search: string) => dispatch({ type: 'search', search }),
    toggleOrder: () => dispatch({ type: 'toggleOrder' }),
    setCategory: (category: AuditCategory) => dispatch({ type: 'category', category }),
    setAction: (action: string) => dispatch({ type: 'action', action }),
    clearFilters: () => dispatch({ type: 'all' }),
    nextPage: (lastId: number) => dispatch({ type: 'next', lastId }),
    prevPage: () => dispatch({ type: 'prev' }),
  }
}

function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const id = setTimeout(() => setSettled(value), ms)
    return () => clearTimeout(id)
  }, [value, ms])
  return settled
}

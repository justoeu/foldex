import type { AuditCategory, AuditQuery, AuditWindow } from '../../api/admin'

/**
 * Filter state for the administrative trail (ADR-46).
 *
 * Pagination is keyset (`pages` = ids already shown), not offset. Any change
 * to what is being FILTERED restarts it: a cursor from the previous result set
 * points at an id this one may not contain.
 */
export type AuditFilterState = {
  period: AuditWindow
  action: string
  category: AuditCategory | ''
  search: string
  oldestFirst: boolean
  pages: number[]
}

export type AuditFilterEvent =
  | { type: 'period'; period: AuditWindow }
  | { type: 'search'; search: string }
  | { type: 'toggleOrder' }
  | { type: 'all' }
  | { type: 'category'; category: AuditCategory }
  | { type: 'action'; action: string }
  | { type: 'inspectIP'; ip: string }
  | { type: 'next'; lastId: number }
  | { type: 'prev' }

export function initialAuditFilters(): AuditFilterState {
  return {
    period: '7d',
    action: '',
    category: '',
    search: '',
    oldestFirst: false,
    pages: [],
  }
}

export function reduceAuditFilters(state: AuditFilterState, event: AuditFilterEvent): AuditFilterState {
  switch (event.type) {
    case 'period':
      return { ...state, period: event.period, pages: [] }
    case 'search':
      return { ...state, search: event.search, pages: [] }
    case 'toggleOrder':
      return { ...state, oldestFirst: !state.oldestFirst, pages: [] }
    case 'all':
      return { ...state, action: '', category: '', pages: [] }
    case 'category': {
      const category = state.category === event.category ? '' : event.category
      return { ...state, category, action: '', pages: [] }
    }
    case 'action': {
      const action = state.action === event.action ? '' : event.action
      return { ...state, action, category: '', pages: [] }
    }
    case 'inspectIP':
      // The trail's search already matches `host(ip)`. Every other filter is
      // cleared with it: an address inspected under a leftover action chip
      // would show a filtered subset of its own events and read as empty.
      return { ...state, search: event.ip, action: '', category: '', pages: [] }
    case 'next':
      return { ...state, pages: [...state.pages, event.lastId] }
    case 'prev':
      return { ...state, pages: state.pages.slice(0, -1) }
  }
}

/**
 * The query the list is read under.
 *
 * Oldest-first is `order: 'asc'` — a different QUERY (INV-179), never the page
 * inverted in the browser. Subject is deliberately absent (INV-175): the
 * administrative projection does not select it, and a client knob would search
 * a column the server withholds.
 */
export function toAuditQuery(state: AuditFilterState, debouncedSearch: string): AuditQuery {
  return {
    window: state.period,
    action: state.action || undefined,
    category: state.category || undefined,
    q: debouncedSearch || undefined,
    before: state.pages.length > 0 ? state.pages[state.pages.length - 1] : undefined,
    order: state.oldestFirst ? 'asc' : undefined,
  }
}

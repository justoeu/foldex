import { describe, expect, it, vi } from 'vitest'
import {
  initialAuditFilters,
  reduceAuditFilters,
  toAuditQuery,
} from './auditFilters'
import { downloadAuditCsv } from './auditExport'

describe('AuditFilterCharter', () => {
  it('inspectIP resets action, category and paging, and searches the address', () => {
    const start = reduceAuditFilters(initialAuditFilters(), { type: 'action', action: 'login.failed' })
    const paged = reduceAuditFilters(start, { type: 'next', lastId: 42 })
    const withCat = reduceAuditFilters(paged, { type: 'category', category: 'identity' })
    expect(withCat.action).toBe('')
    expect(withCat.category).toBe('identity')
    expect(withCat.pages).toEqual([])

    const inspected = reduceAuditFilters(
      { ...withCat, pages: [9], action: 'login.failed' },
      { type: 'inspectIP', ip: '189.42.11.7' },
    )
    expect(inspected).toMatchObject({
      search: '189.42.11.7',
      action: '',
      category: '',
      pages: [],
    })
  })

  it('category and action chips are exclusive, never AND-composed', () => {
    const cat = reduceAuditFilters(initialAuditFilters(), { type: 'category', category: 'content' })
    expect(cat.category).toBe('content')
    expect(cat.action).toBe('')

    const act = reduceAuditFilters(cat, { type: 'action', action: 'link.created' })
    expect(act.action).toBe('link.created')
    expect(act.category).toBe('')

    const back = reduceAuditFilters(act, { type: 'category', category: 'identity' })
    expect(back.category).toBe('identity')
    expect(back.action).toBe('')

    const cleared = reduceAuditFilters(back, { type: 'all' })
    expect(cleared.category).toBe('')
    expect(cleared.action).toBe('')
  })

  // INV-179: ascending is a different QUERY, never the inverted page.
  it('oldest-first is order=asc on the query and resets the keyset cursor', () => {
    const paged = reduceAuditFilters(initialAuditFilters(), { type: 'next', lastId: 7 })
    const flipped = reduceAuditFilters(paged, { type: 'toggleOrder' })
    expect(flipped.oldestFirst).toBe(true)
    expect(flipped.pages).toEqual([])
    const q = toAuditQuery(flipped, '')
    expect(q.order).toBe('asc')
    expect(q.before).toBeUndefined()
    expect(q).not.toHaveProperty('entries')
  })

  // INV-175: the administrative projection does not select subject. The query
  // builder must not grow a knob that would search it.
  it('never puts a content subject on the administrative query', () => {
    const q = toAuditQuery(
      reduceAuditFilters(initialAuditFilters(), { type: 'category', category: 'content' }),
      '',
    )
    expect(q).toEqual({ window: '7d', category: 'content' })
    expect(q).not.toHaveProperty('subject')
  })

  it('reports export failure instead of leaving a leaked object URL', async () => {
    const createObjectURL = vi.fn(() => 'blob:leaked')
    const revokeObjectURL = vi.fn()
    await expect(downloadAuditCsv(
      { window: '7d' },
      {
        exportCsv: async () => { throw new Error('boom') },
        createObjectURL,
        revokeObjectURL,
      },
    )).rejects.toThrow('boom')
    expect(createObjectURL).not.toHaveBeenCalled()
    expect(revokeObjectURL).not.toHaveBeenCalled()
  })

  it('revokes the object URL after a successful save', async () => {
    const createObjectURL = vi.fn(() => 'blob:ok')
    const revokeObjectURL = vi.fn()
    const click = vi.fn()
    const a = { href: '', download: '', click, remove: vi.fn() } as unknown as HTMLAnchorElement
    const doc = {
      createElement: () => a,
      body: { appendChild: vi.fn() },
    } as unknown as Document

    await downloadAuditCsv(
      { window: '24h', order: 'asc' },
      {
        exportCsv: async () => new Blob(['id\n']),
        createObjectURL,
        revokeObjectURL,
        doc,
        now: () => new Date('2026-04-01T12:00:00Z'),
      },
    )
    expect(createObjectURL).toHaveBeenCalled()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:ok')
    expect(a.download).toBe('foldex-audit-2026-04-01.csv')
  })
})

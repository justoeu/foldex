import { describe, expect, it } from 'vitest'
import { buildActivityHistogram, groupActivityEntriesByDay, CHART_DAYS } from './activityModel'
import type { AuditEntry } from '../../api/admin'

function entry(id: number, createdAt: string, action = 'link.created'): AuditEntry {
  return {
    id,
    action,
    created_at: createdAt,
    subject: `Item ${id}`,
  } as AuditEntry
}

const t = ((key: string) => key) as never

describe('buildActivityHistogram', () => {
  it('buckets entries into the trailing window and zero-fills empty days', () => {
    const now = new Date('2026-06-15T15:00:00')
    const days = buildActivityHistogram(
      [entry(1, '2026-06-15T10:00:00'), entry(2, '2026-06-15T11:00:00'), entry(3, '2026-06-02T10:00:00'), entry(4, '2026-05-01T10:00:00')],
      'en-US',
      now,
    )
    expect(days).toHaveLength(CHART_DAYS)
    expect(days.at(-1)).toMatchObject({ key: '2026-06-15', count: 2 })
    expect(days[0]).toMatchObject({ key: '2026-06-02', count: 1 })
    expect(days.reduce((acc, d) => acc + d.count, 0)).toBe(3)
  })
})

describe('groupActivityEntriesByDay', () => {
  it('groups newest day first with per-row display fields', () => {
    const groups = groupActivityEntriesByDay(
      [entry(2, '2026-06-14T09:00:00'), entry(1, '2026-06-15T09:00:00'), entry(3, '2026-06-15T10:00:00', 'link.deleted')],
      'en-US',
      t,
    )
    expect(groups.map((g) => g.key)).toEqual(['2026-06-15', '2026-06-14'])
    expect(groups[0].items).toHaveLength(2)
    expect(groups[0].items[1].dot).toBe('fx-acc2-act-dot-delete')
  })
})

import { describe, expect, it } from 'vitest'
import { dropSourceForLink, hasUnseenChange } from './LinkCardInteractions'
import type { Link } from '../api/types'

function transfer(link = '', note = ''): Pick<DataTransfer, 'getData'> {
  return {
    getData: (type) => type === 'application/x-foldex-link' ? link : note,
  }
}

describe('LinkCard interaction contracts', () => {
  it('parses link and note merge sources while preserving link precedence', () => {
    expect(dropSourceForLink(transfer('7'), 99)).toEqual({ kind: 'link', id: 7 })
    expect(dropSourceForLink(transfer('', '3'), 99)).toEqual({ kind: 'note', id: 3 })
    expect(dropSourceForLink(transfer('7', '3'), 99)).toEqual({ kind: 'link', id: 7 })
  })

  it('rejects empty, invalid, and same-link drops', () => {
    expect(dropSourceForLink(transfer(), 99)).toBeNull()
    expect(dropSourceForLink(transfer('not-a-number'), 99)).toBeNull()
    expect(dropSourceForLink(transfer('99'), 99)).toBeNull()
  })

  it('shows an unseen badge only when detection is newer than acknowledgement', () => {
    const link = {
      last_change_detected_at: '2026-05-30T10:00:00Z',
      change_seen_at: '2026-05-29T10:00:00Z',
    } as Link
    expect(hasUnseenChange(link)).toBe(true)
    expect(hasUnseenChange({ ...link, change_seen_at: '2026-05-31T10:00:00Z' })).toBe(false)
    expect(hasUnseenChange({ ...link, last_change_detected_at: null })).toBe(false)
  })
})

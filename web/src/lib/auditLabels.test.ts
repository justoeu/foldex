import { describe, expect, it } from 'vitest'
import { actionCategory } from './auditLabels'

describe('actionCategory', () => {
  it('classifies the canonical <entity>.<verb> vocabulary by suffix', () => {
    expect(actionCategory('link.created')).toBe('create')
    expect(actionCategory('note.updated')).toBe('edit')
    expect(actionCategory('folder.deleted')).toBe('delete')
    expect(actionCategory('link.clicked')).toBe('click')
    expect(actionCategory('login.succeeded')).toBe('login')
  })

  it('treats revocations as deletions and unknown actions as other', () => {
    expect(actionCategory('share.revoked')).toBe('delete')
    expect(actionCategory('api_token.revoked')).toBe('delete')
    expect(actionCategory('backup.restored')).toBe('other')
  })

  it('keeps legacy bare-verb rows classified', () => {
    expect(actionCategory('create')).toBe('create')
    expect(actionCategory('update')).toBe('edit')
    expect(actionCategory('delete')).toBe('delete')
  })
})

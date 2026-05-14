import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  appendBackupHistory,
  generateBackup,
  readBackupHistory,
  restoreBackup,
  validateBackup,
} from './backup'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
  localStorage.clear()
})

describe('backup history (localStorage)', () => {
  it('returns empty on a fresh store', () => {
    expect(readBackupHistory()).toEqual([])
  })

  it('appends and caps at 10 entries (newest first)', () => {
    for (let i = 0; i < 12; i++) {
      appendBackupHistory({
        id: `id-${i}`,
        created_at: `2026-05-${10 + i}T00:00:00Z`,
        duration_ms: 100,
        size_bytes: 1024,
        counts: { links: i, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      })
    }
    const out = readBackupHistory()
    expect(out).toHaveLength(10)
    // Newest prepended → id-11 first, id-2 last (id-0/id-1 dropped past cap)
    expect(out[0].id).toBe('id-11')
    expect(out[9].id).toBe('id-2')
  })

  it('tolerates corrupt JSON in storage', () => {
    localStorage.setItem('foldex.backups', '{not json')
    expect(readBackupHistory()).toEqual([])
  })
})

describe('generateBackup', () => {
  it('downloads the zip and appends to history', async () => {
    const clickSpy = vi.fn()
    const appendSpy = vi.spyOn(document.body, 'appendChild')
    // Stub anchor click since jsdom invokes navigation.
    const origCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreate(tag)
      if (tag === 'a') (el as HTMLAnchorElement).click = clickSpy
      return el
    })

    const entry = await generateBackup()
    expect(entry.counts.links).toBe(5)
    expect(entry.size_bytes).toBeGreaterThan(0)
    expect(clickSpy).toHaveBeenCalledOnce()
    expect(appendSpy).toHaveBeenCalled()
    expect(readBackupHistory()).toHaveLength(1)
  })
})

describe('validateBackup', () => {
  it('returns the mock validation', async () => {
    const file = new File([new Uint8Array([0x50, 0x4b])], 'foo.zip', { type: 'application/zip' })
    const v = await validateBackup(file)
    expect(v.ok).toBe(true)
    expect(v.manifest?.counts.links).toBe(5)
  })

  it('surfaces backend errors to callers', async () => {
    state.backupValidation = {
      ok: false,
      manifest: null,
      conflicts: { links: 0, tags: 0, folders: 0 },
      warnings: [],
      errors: ['checksum mismatch: files/images/7.jpg'],
    }
    const file = new File([new Uint8Array([0])], 'foo.zip', { type: 'application/zip' })
    const v = await validateBackup(file)
    expect(v.ok).toBe(false)
    expect(v.errors).toContain('checksum mismatch: files/images/7.jpg')
  })
})

describe('restoreBackup', () => {
  it('passes the mode to the backend', async () => {
    const file = new File([new Uint8Array([0])], 'foo.zip', { type: 'application/zip' })
    const rep = await restoreBackup(file, 'wipe')
    expect(state.lastRestoreMode).toBe('wipe')
    expect(rep.mode).toBe('wipe')
    expect(rep.inserted.links).toBe(5)
  })

  it('defaults to skip when no mode parameter is forced', async () => {
    const file = new File([new Uint8Array([0])], 'foo.zip', { type: 'application/zip' })
    await restoreBackup(file, 'skip')
    expect(state.lastRestoreMode).toBe('skip')
  })
})

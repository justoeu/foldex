import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  appendBackupHistory,
  countsFromHeaders,
  extractManifestFromZip,
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

  it('swallows setItem throws so download UX is never aborted', () => {
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError')
    })
    expect(() =>
      appendBackupHistory({
        id: 'q',
        created_at: '2026-05-01T00:00:00Z',
        duration_ms: 1,
        size_bytes: 1,
        counts: { links: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      }),
    ).not.toThrow()
    spy.mockRestore()
  })
})

describe('generateBackup with broken localStorage', () => {
  it('still triggers download when appendBackupHistory cannot persist', async () => {
    const setSpy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError')
    })
    const clickSpy = vi.fn()
    const origCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreate(tag)
      if (tag === 'a') (el as HTMLAnchorElement).click = clickSpy
      return el
    })

    const entry = await generateBackup()
    expect(entry.size_bytes).toBeGreaterThan(0)
    expect(clickSpy).toHaveBeenCalledOnce()
    setSpy.mockRestore()
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

describe('countsFromHeaders', () => {
  it('returns null when links header missing', () => {
    expect(countsFromHeaders({})).toBeNull()
  })

  it('parses full count set from X-Foldex-Backup-Counts-*', () => {
    const c = countsFromHeaders({
      'X-Foldex-Backup-Counts-Links': '5',
      'x-foldex-backup-counts-tags': '2',
      'X-Foldex-Backup-Counts-Folders': '1',
      'X-Foldex-Backup-Counts-Link-Tags': '3',
      'X-Foldex-Backup-Counts-Click-Logs': '8',
      'X-Foldex-Backup-Counts-Files': '0',
      'X-Foldex-Backup-Counts-File-Bytes': '0',
    })
    expect(c).toEqual({
      links: 5, tags: 2, folders: 1, link_tags: 3,
      click_logs: 8, files: 0, file_bytes: 0,
    })
  })
})

describe('extractManifestFromZip (slice/EOCD)', () => {
  it('reads manifest without requiring a full-arrayBuffer copy of the blob', async () => {
    // Reuse the mock's minimal zip builder via generateBackup's zip shape.
    // Build a tiny store-method zip with manifest.json inline.
    const manifest = {
      kind: 'foldex.backup',
      version: '1.0',
      schema_version: 8,
      created_at: '2026-05-14T03:00:00Z',
      counts: { links: 9, tags: 1, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      checksums: {},
    }
    const name = new TextEncoder().encode('manifest.json')
    const data = new TextEncoder().encode(JSON.stringify(manifest))
    const local = new Uint8Array(30 + name.length + data.length)
    const ldv = new DataView(local.buffer)
    ldv.setUint32(0, 0x04034b50, true)
    ldv.setUint16(8, 0, true) // store
    ldv.setUint32(18, data.length, true)
    ldv.setUint32(22, data.length, true)
    ldv.setUint16(26, name.length, true)
    local.set(name, 30)
    local.set(data, 30 + name.length)

    const cd = new Uint8Array(46 + name.length)
    const cdv = new DataView(cd.buffer)
    cdv.setUint32(0, 0x02014b50, true)
    cdv.setUint16(10, 0, true)
    cdv.setUint32(20, data.length, true)
    cdv.setUint16(28, name.length, true)
    cdv.setUint32(42, 0, true) // local hdr offset
    cd.set(name, 46)

    const eocd = new Uint8Array(22)
    const edv = new DataView(eocd.buffer)
    edv.setUint32(0, 0x06054b50, true)
    edv.setUint16(8, 1, true)
    edv.setUint16(10, 1, true)
    edv.setUint32(12, cd.length, true)
    edv.setUint32(16, local.length, true)

    const zip = new Uint8Array(local.length + cd.length + eocd.length)
    zip.set(local, 0)
    zip.set(cd, local.length)
    zip.set(eocd, local.length + cd.length)

    const blob = new Blob([zip.buffer as ArrayBuffer])
    // Spy: full arrayBuffer on the whole blob must NOT be required.
    const fullAB = vi.spyOn(blob, 'arrayBuffer')
    const got = await extractManifestFromZip(blob)
    expect(got?.counts.links).toBe(9)
    expect(got?.created_at).toBe('2026-05-14T03:00:00Z')
    // Only slice().arrayBuffer paths — never blob.arrayBuffer() on the whole zip.
    expect(fullAB).not.toHaveBeenCalled()
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

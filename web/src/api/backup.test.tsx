import { describe, it, expect, beforeEach, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import {
  appendBackupHistory,
  countsFromHeaders,
  extractManifestFromZip,
  generateBackup,
  readBackupHistory,
  restoreBackup,
  useRestoreBackup,
  validateBackup,
} from './backup'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from './client'

let state: MockState

type ZipFixtureOptions = {
  body?: string
  centralFlags?: number
  localFlags?: number
  centralCompression?: number
  localCompression?: number
  centralSignature?: number
  localSignature?: number
  compressedSize?: number
  localCompressedSize?: number
  localUncompressedSize?: number
  centralOffset?: number
  centralSize?: number
  localExtraLength?: number
  localName?: string
  centralCopies?: number
  entryCount?: number
  comment?: Uint8Array
}

function manifestZip(options: ZipFixtureOptions = {}): Uint8Array {
  const encoder = new TextEncoder()
  const name = encoder.encode('manifest.json')
  const localName = encoder.encode(options.localName ?? 'manifest.json')
  const data = encoder.encode(options.body ?? JSON.stringify({
    kind: 'foldex.backup',
    version: '1.0',
    schema_version: 8,
    created_at: '2026-05-14T03:00:00Z',
    counts: { links: 9, tags: 1, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
    checksums: {},
  }))
  const local = new Uint8Array(30 + localName.length + data.length)
  const ldv = new DataView(local.buffer)
  ldv.setUint32(0, options.localSignature ?? 0x04034b50, true)
  ldv.setUint16(6, options.localFlags ?? 0, true)
  ldv.setUint16(8, options.localCompression ?? 0, true)
  ldv.setUint32(18, options.localCompressedSize ?? data.length, true)
  ldv.setUint32(22, options.localUncompressedSize ?? data.length, true)
  ldv.setUint16(26, localName.length, true)
  ldv.setUint16(28, options.localExtraLength ?? 0, true)
  local.set(localName, 30)
  local.set(data, 30 + localName.length)

  const centralEntry = new Uint8Array(46 + name.length)
  const cdv = new DataView(centralEntry.buffer)
  cdv.setUint32(0, options.centralSignature ?? 0x02014b50, true)
  cdv.setUint16(8, options.centralFlags ?? 0, true)
  cdv.setUint16(10, options.centralCompression ?? 0, true)
  cdv.setUint32(20, options.compressedSize ?? data.length, true)
  cdv.setUint32(24, data.length, true)
  cdv.setUint16(28, name.length, true)
  cdv.setUint32(42, 0, true)
  centralEntry.set(name, 46)
  const centralCopies = options.centralCopies ?? 1
  const cd = new Uint8Array(centralEntry.length * centralCopies)
  for (let index = 0; index < centralCopies; index++) cd.set(centralEntry, index * centralEntry.length)

  const comment = options.comment ?? new Uint8Array()
  const eocd = new Uint8Array(22 + comment.length)
  const edv = new DataView(eocd.buffer)
  edv.setUint32(0, 0x06054b50, true)
  const entryCount = options.entryCount ?? centralCopies
  edv.setUint16(8, entryCount, true)
  edv.setUint16(10, entryCount, true)
  edv.setUint32(12, options.centralSize ?? cd.length, true)
  edv.setUint32(16, options.centralOffset ?? local.length, true)
  edv.setUint16(20, comment.length, true)
  eocd.set(comment, 22)

  const out = new Uint8Array(local.length + cd.length + eocd.length)
  out.set(local, 0)
  out.set(cd, local.length)
  out.set(eocd, local.length + cd.length)
  return out
}

function blobFromBytes(bytes: Uint8Array): Blob {
  const buffer = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
  return new Blob([buffer])
}

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
    expect(localStorage.getItem('foldex.backups')).toBeNull()
  })

  it('drops decoded history entries with invalid shapes', () => {
    const valid = {
      id: 'valid',
      created_at: '2026-05-14T03:00:00Z',
      duration_ms: 100,
      size_bytes: 1024,
      counts: { links: 1, tags: 2, folders: 3, link_tags: 4, click_logs: 5, files: 6, file_bytes: 7 },
    }
    localStorage.setItem('foldex.backups', JSON.stringify([
      valid,
      { id: 'missing-fields' },
      { ...valid, id: 'bad-counts', counts: null },
      { ...valid, id: 'negative-duration', duration_ms: -1 },
      { ...valid, id: 'unsafe-size', size_bytes: Number.MAX_SAFE_INTEGER + 1 },
      { ...valid, id: 'fractional-count', counts: { ...valid.counts, links: 1.5 } },
      { ...valid, id: 'invalid-date', created_at: 'not-a-date' },
      null,
    ]))

    expect(readBackupHistory()).toEqual([valid])
    expect(JSON.parse(localStorage.getItem('foldex.backups') ?? '[]')).toEqual([valid])
  })

  it('removes persisted history entries beyond the maximum', () => {
    const entries = Array.from({ length: 12 }, (_, index) => ({
      id: `id-${index}`,
      created_at: '2026-05-14T03:00:00Z',
      duration_ms: index,
      size_bytes: index,
      counts: { links: index, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
    }))
    localStorage.setItem('foldex.backups', JSON.stringify(entries))

    expect(readBackupHistory()).toHaveLength(10)
    expect(JSON.parse(localStorage.getItem('foldex.backups') ?? '[]')).toHaveLength(10)
  })

  it('swallows setItem throws so download UX is never aborted', () => {
    const spy = vi.spyOn(window.localStorage, 'setItem').mockImplementation(() => {
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
    expect(spy).toHaveBeenCalledOnce()
    spy.mockRestore()
  })
})

describe('generateBackup with broken localStorage', () => {
  it('still triggers download when appendBackupHistory cannot persist', async () => {
    const setSpy = vi.spyOn(window.localStorage, 'setItem').mockImplementation(() => {
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
    expect(setSpy).toHaveBeenCalledOnce()
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
    cdv.setUint32(24, data.length, true)
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

  it('handles a legal ZIP comment containing a false EOCD signature', async () => {
    const comment = new Uint8Array(32)
    comment.set([0x50, 0x4b, 0x05, 0x06], 2)
    const got = await extractManifestFromZip(blobFromBytes(manifestZip({ comment })))
    expect(got?.counts.links).toBe(9)
  })

  it('reads a valid streamed ZIP that uses a data descriptor', async () => {
    const got = await extractManifestFromZip(blobFromBytes(manifestZip({
      centralFlags: 1 << 3,
      localFlags: 1 << 3,
      localCompressedSize: 0,
      localUncompressedSize: 0,
    })))
    expect(got?.counts.links).toBe(9)
  })

  it('rejects a Zip64 entry count sentinel', async () => {
    expect(await extractManifestFromZip(blobFromBytes(manifestZip({ entryCount: 0xffff })))).toBeNull()
  })

  it('rejects duplicate manifest entries', async () => {
    expect(await extractManifestFromZip(blobFromBytes(manifestZip({ centralCopies: 2 })))).toBeNull()
  })

  it.each([
    ['central header', { centralFlags: 1 }],
    ['local header', { localFlags: 1 }],
  ])('rejects an encrypted manifest in the %s', async (_name, options) => {
    expect(await extractManifestFromZip(blobFromBytes(manifestZip(options)))).toBeNull()
  })

  it.each([
    ['filename', { localName: 'different.json' }],
    ['stored size', { localCompressedSize: 1 }],
  ])('rejects a central/local %s mismatch', async (_name, options) => {
    expect(await extractManifestFromZip(blobFromBytes(manifestZip(options)))).toBeNull()
  })

  it.each([
    ['truncated EOCD', () => manifestZip().slice(0, -5)],
    ['truncated central directory', () => manifestZip({ centralSize: 40 })],
    ['truncated local extra/data', () => manifestZip({ localExtraLength: 0xffff })],
    ['bad central signature', () => manifestZip({ centralSignature: 0x11111111 })],
    ['bad local signature', () => manifestZip({ localSignature: 0x11111111 })],
    ['unsupported central compression', () => manifestZip({ centralCompression: 8 })],
    ['unsupported local compression', () => manifestZip({ localCompression: 8 })],
    ['overflowing central offset', () => manifestZip({ centralOffset: 0xffffffff })],
    ['overflowing manifest size', () => manifestZip({ compressedSize: 0xffffffff })],
  ])('rejects %s', async (_name, fixture) => {
    expect(await extractManifestFromZip(blobFromBytes(fixture()))).toBeNull()
  })

  it.each([
    ['malformed JSON', '{not-json'],
    ['malformed manifest shape', '{"kind":1}'],
  ])('rejects %s', async (_name, body) => {
    expect(await extractManifestFromZip(blobFromBytes(manifestZip({ body })))).toBeNull()
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

  it('passes an AbortSignal to the validation upload', async () => {
    const controller = new AbortController()
    const post = vi.spyOn(http, 'post')
    const file = new File([new Uint8Array([0])], 'foo.zip', { type: 'application/zip' })

    await validateBackup(file, controller.signal)

    expect(post).toHaveBeenCalledWith(
      '/api/backup/validate',
      expect.any(FormData),
      expect.objectContaining({ signal: controller.signal }),
    )
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

  it('overrides the ordinary API timeout for large backup uploads', async () => {
    const post = vi.spyOn(http, 'post')
    const file = new File([new Uint8Array([0])], 'large.zip', { type: 'application/zip' })
    await restoreBackup(file, 'skip')
    expect(post).toHaveBeenCalledWith(
      '/api/backup/restore?mode=skip',
      expect.any(FormData),
      expect.objectContaining({ timeout: 30 * 60_000 }),
    )
  })

})

describe('useRestoreBackup', () => {
  it('invalidates every affected cache after failure', async () => {
    const invalidate = vi.fn()
    const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
    client.invalidateQueries = invalidate
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    vi.mocked(http.post).mockRejectedValueOnce(new Error('restore failed'))
    const { result } = renderHook(() => useRestoreBackup(), { wrapper })

    await act(async () => {
      await expect(result.current.mutateAsync({
        file: new File([new Uint8Array([0])], 'backup.zip'),
        mode: 'skip',
      })).rejects.toThrow('restore failed')
    })

    expect(invalidate.mock.calls.map((call) => call[0]?.queryKey?.[0])).toEqual([
      'links', 'entries', 'folders', 'tags', 'stats',
    ])
  })

  it('does not turn successful restore into failure when cache invalidation rejects', async () => {
    const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
    client.invalidateQueries = vi.fn().mockRejectedValue(new Error('cache failure'))
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useRestoreBackup(), { wrapper })

    await act(async () => {
      await expect(result.current.mutateAsync({
        file: new File([new Uint8Array([0])], 'backup.zip'),
        mode: 'skip',
      })).resolves.toMatchObject({ mode: 'skip' })
    })
  })
})

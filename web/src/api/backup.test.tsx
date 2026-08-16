import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import {
  appendBackupHistory,
  countsFromHeaders,
  generateBackup,
  readBackupHistory,
  restoreBackup,
  useRestoreBackup,
  validateBackup,
} from './backup'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from './client'

let state: MockState

const downloadCounts = {
  links: 5, notes: 4, tags: 2, folders: 1, link_tags: 3,
  click_logs: 8, files: 1, file_bytes: 1024,
}

function mockNavigationTicket() {
  vi.mocked(http.post).mockResolvedValueOnce({
    data: {
      id: 'download-id',
      download_url: '/api/backup/download?id=download-id&token=one-time-token',
      status_url: '/api/backup/download/status?id=download-id',
      filename: 'foldex-backup-20260514T030000Z.zip',
      created_at: '2026-05-14T03:00:00Z',
      expires_at: '2026-05-14T03:01:00Z',
    },
  } as never)
}

function downloadStatus(
  state: 'pending' | 'running' | 'complete' | 'failed',
  error?: { code: string; message: string },
) {
  return {
    data: {
      id: 'download-id',
      state,
      created_at: '2026-05-14T03:00:00Z',
      duration_ms: state === 'complete' ? 42 : 0,
      size_bytes: state === 'complete' ? 4096 : 0,
      counts: state === 'complete' ? downloadCounts : {
        links: 0, notes: 0, tags: 0, folders: 0, link_tags: 0,
        click_logs: 0, files: 0, file_bytes: 0,
      },
      ...(error ? { error } : {}),
    },
  }
}

function mockCompletedNavigationDownload() {
  mockNavigationTicket()
  vi.mocked(http.get).mockResolvedValueOnce({
    ...downloadStatus('complete'),
  } as never)
}

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
  localStorage.clear()
})

afterEach(() => {
  vi.useRealTimers()
  delete (window as Window & { showSaveFilePicker?: unknown }).showSaveFilePicker
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
        counts: { links: i, notes: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
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
      counts: { links: 1, notes: 8, tags: 2, folders: 3, link_tags: 4, click_logs: 5, files: 6, file_bytes: 7 },
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

  it('upgrades persisted history written before note counts existed', () => {
    const legacy = {
      id: 'legacy',
      created_at: '2026-05-14T03:00:00Z',
      duration_ms: 100,
      size_bytes: 1024,
      counts: { links: 1, tags: 2, folders: 3, link_tags: 4, click_logs: 5, files: 6, file_bytes: 7 },
    }
    localStorage.setItem('foldex.backups', JSON.stringify([legacy]))

    expect(readBackupHistory()[0]?.counts.notes).toBe(0)
    expect(JSON.parse(localStorage.getItem('foldex.backups') ?? '[]')[0].counts.notes).toBe(0)
  })

  it('removes persisted history entries beyond the maximum', () => {
    const entries = Array.from({ length: 12 }, (_, index) => ({
      id: `id-${index}`,
      created_at: '2026-05-14T03:00:00Z',
      duration_ms: index,
      size_bytes: index,
      counts: { links: index, notes: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
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
        counts: { links: 0, notes: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      }),
    ).not.toThrow()
    expect(spy).toHaveBeenCalledOnce()
    spy.mockRestore()
  })
})

describe('generateBackup with broken localStorage', () => {
  it('still triggers download when appendBackupHistory cannot persist', async () => {
    mockCompletedNavigationDownload()
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
  it('streams the response into a browser-selected file and records history', async () => {
    const writes: Uint8Array[] = []
    const writer = {
      write: vi.fn(async (chunk: Uint8Array) => { writes.push(chunk) }),
      close: vi.fn(async () => undefined),
      abort: vi.fn(async () => undefined),
    }
    Object.defineProperty(window, 'showSaveFilePicker', {
      configurable: true,
      value: vi.fn(async () => ({ createWritable: async () => ({ getWriter: () => writer }) })),
    })
    const reader = {
      read: vi.fn()
        .mockResolvedValueOnce({ done: false, value: new Uint8Array([1, 2]) })
        .mockResolvedValueOnce({ done: false, value: new Uint8Array([3, 4, 5]) })
        .mockResolvedValueOnce({ done: true, value: undefined }),
      cancel: vi.fn(async () => undefined),
    }
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      headers: new Headers({
        'X-Foldex-Backup-Filename': 'foldex-backup-20260514T030000Z.zip',
        'X-Foldex-Backup-Counts-Links': '5',
        'X-Foldex-Backup-Counts-Notes': '4',
        'X-Foldex-Backup-Counts-Tags': '2',
        'X-Foldex-Backup-Counts-Folders': '1',
      }),
      body: { getReader: () => reader },
    } as unknown as Response)
    const objectURL = vi.spyOn(URL, 'createObjectURL')

    const entry = await generateBackup()

    expect(fetchSpy).toHaveBeenCalledWith('/api/backup', expect.objectContaining({
      method: 'POST',
      credentials: 'include',
    }))
    expect(writes.map((chunk) => [...chunk])).toEqual([[1, 2], [3, 4, 5]])
    expect(writer.close).toHaveBeenCalledOnce()
    expect(objectURL).not.toHaveBeenCalled()
    expect(entry).toMatchObject({
      created_at: '2026-05-14T03:00:00Z',
      size_bytes: 5,
      counts: { links: 5, notes: 4, tags: 2, folders: 1 },
    })
    expect(readBackupHistory()).toEqual([entry])
  })

  it('cancels the response and file writer when a streamed write fails', async () => {
    const failure = new Error('disk full')
    const writer = {
      write: vi.fn().mockRejectedValue(failure),
      close: vi.fn(async () => undefined),
      abort: vi.fn(async () => undefined),
    }
    Object.defineProperty(window, 'showSaveFilePicker', {
      configurable: true,
      value: vi.fn(async () => ({ createWritable: async () => ({ getWriter: () => writer }) })),
    })
    const reader = {
      read: vi.fn().mockResolvedValue({ done: false, value: new Uint8Array([1]) }),
      cancel: vi.fn(async () => undefined),
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      headers: new Headers({ 'X-Foldex-Backup-Counts-Links': '1' }),
      body: { getReader: () => reader },
    } as unknown as Response)

    await expect(generateBackup()).rejects.toThrow('disk full')

    expect(reader.cancel).toHaveBeenCalledWith(failure)
    expect(writer.abort).toHaveBeenCalledWith(failure)
    expect(writer.close).not.toHaveBeenCalled()
    expect(readBackupHistory()).toEqual([])
  })

  it('uses a one-time native navigation without constructing a Blob', async () => {
    mockCompletedNavigationDownload()
    const clickSpy = vi.fn()
    const appendSpy = vi.spyOn(document.body, 'appendChild')
    const objectURL = vi.spyOn(URL, 'createObjectURL')
    const realBlob = globalThis.Blob
    const blobConstructor = vi.fn()
    Object.defineProperty(globalThis, 'Blob', { configurable: true, writable: true, value: blobConstructor })
    // Stub anchor click since jsdom invokes navigation.
    const origCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreate(tag)
      if (tag === 'a') (el as HTMLAnchorElement).click = clickSpy
      return el
    })

    const entry = await generateBackup().finally(() => {
      Object.defineProperty(globalThis, 'Blob', { configurable: true, writable: true, value: realBlob })
    })
    expect(entry.counts.links).toBe(5)
    expect(entry.size_bytes).toBe(4096)
    expect(http.post).toHaveBeenCalledWith('/api/backup/download')
    expect(http.get).toHaveBeenCalledWith('/api/backup/download/status?id=download-id')
    expect(clickSpy).toHaveBeenCalledOnce()
    expect(appendSpy).toHaveBeenCalled()
    const anchor = vi.mocked(document.createElement).mock.results.find((result) =>
      result.value instanceof HTMLAnchorElement)?.value as HTMLAnchorElement
    expect(anchor.href).toContain('/api/backup/download?id=download-id&token=one-time-token')
    expect(anchor.referrerPolicy).toBe('no-referrer')
    expect(blobConstructor).not.toHaveBeenCalled()
    expect(objectURL).not.toHaveBeenCalled()
    expect(readBackupHistory()).toHaveLength(1)
  })

  it('polls pending to running to complete and records history only after completion', async () => {
    vi.useFakeTimers()
    mockNavigationTicket()
    vi.mocked(http.get)
      .mockResolvedValueOnce(downloadStatus('pending') as never)
      .mockResolvedValueOnce(downloadStatus('running') as never)
      .mockResolvedValueOnce(downloadStatus('complete') as never)
    const origCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreate(tag)
      if (tag === 'a') (el as HTMLAnchorElement).click = vi.fn()
      return el
    })

    const result = generateBackup()
    await vi.advanceTimersByTimeAsync(0)
    expect(http.get).toHaveBeenCalledTimes(1)
    expect(readBackupHistory()).toEqual([])

    await vi.advanceTimersByTimeAsync(1_000)
    expect(http.get).toHaveBeenCalledTimes(2)
    expect(readBackupHistory()).toEqual([])

    await vi.advanceTimersByTimeAsync(1_999)
    expect(http.get).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    await expect(result).resolves.toMatchObject({ id: 'download-id', size_bytes: 4096 })
    expect(readBackupHistory()).toHaveLength(1)
  })

  it('surfaces a failed native download after polling and does not write history', async () => {
    vi.useFakeTimers()
    mockNavigationTicket()
    vi.mocked(http.get)
      .mockResolvedValueOnce(downloadStatus('pending') as never)
      .mockResolvedValueOnce(downloadStatus('failed', {
        code: 'backup_busy', message: 'another backup archive operation is in progress',
      }) as never)
    const origCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreate(tag)
      if (tag === 'a') (el as HTMLAnchorElement).click = vi.fn()
      return el
    })

    const result = generateBackup()
    const rejection = expect(result).rejects.toThrow('another backup archive operation is in progress')
    await vi.advanceTimersByTimeAsync(0)
    expect(readBackupHistory()).toEqual([])
    await vi.advanceTimersByTimeAsync(1_000)
    await rejection
    expect(http.get).toHaveBeenCalledTimes(2)
    expect(readBackupHistory()).toEqual([])
  })

  it('times out with bounded progressive polling and does not write history', async () => {
    vi.useFakeTimers()
    mockNavigationTicket()
    vi.mocked(http.get).mockResolvedValue(downloadStatus('pending') as never)
    const origCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreate(tag)
      if (tag === 'a') (el as HTMLAnchorElement).click = vi.fn()
      return el
    })

    const result = generateBackup()
    const rejection = expect(result).rejects.toThrow('backup download timed out')
    await vi.advanceTimersByTimeAsync(30 * 60_000)
    await rejection
    expect(vi.mocked(http.get).mock.calls.length).toBeLessThan(200)
    expect(readBackupHistory()).toEqual([])
  })

  it('refuses a cross-origin download URL before creating a navigation', async () => {
    vi.mocked(http.post).mockResolvedValueOnce({
      data: {
        id: 'download-id',
        download_url: 'https://attacker.test/backup.zip',
        status_url: '/api/backup/download/status?id=download-id',
        filename: 'backup.zip',
        created_at: '2026-05-14T03:00:00Z',
        expires_at: '2026-05-14T03:01:00Z',
      },
    } as never)
    const createElement = vi.spyOn(document, 'createElement')

    await expect(generateBackup()).rejects.toThrow('backup download URL must be same-origin')
    expect(createElement).not.toHaveBeenCalled()
    expect(http.get).not.toHaveBeenCalled()
  })
})

describe('countsFromHeaders', () => {
  it('returns null when links header missing', () => {
    expect(countsFromHeaders({})).toBeNull()
  })

  it('parses full count set from X-Foldex-Backup-Counts-*', () => {
    const c = countsFromHeaders({
      'X-Foldex-Backup-Counts-Links': '5',
      'X-Foldex-Backup-Counts-Notes': '4',
      'x-foldex-backup-counts-tags': '2',
      'X-Foldex-Backup-Counts-Folders': '1',
      'X-Foldex-Backup-Counts-Link-Tags': '3',
      'X-Foldex-Backup-Counts-Click-Logs': '8',
      'X-Foldex-Backup-Counts-Files': '0',
      'X-Foldex-Backup-Counts-File-Bytes': '0',
    })
    expect(c).toEqual({
      links: 5, notes: 4, tags: 2, folders: 1, link_tags: 3,
      click_logs: 8, files: 0, file_bytes: 0,
    })
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
      'links', 'entries', 'folders', 'tags', 'stats', 'entry-counts',
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

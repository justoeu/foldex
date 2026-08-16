import { useMutation, useQueryClient } from '@tanstack/react-query'
import { authenticatedFetch, http } from './client'
import { invalidateEntryCounts } from './entries'

export type BackupManifest = {
  kind: string
  version: string
  schema_version: number
  created_at: string
  foldex_version?: string
  counts: {
    links: number
    notes: number
    tags: number
    folders: number
    link_tags: number
    click_logs: number
    files: number
    file_bytes: number
  }
  checksums: Record<string, string>
}

export type BackupValidation = {
  ok: boolean
  manifest?: BackupManifest
  conflicts: { links: number; tags: number; folders: number }
  warnings: string[]
  errors: string[]
}

export type RestoreReport = {
  mode: 'wipe' | 'skip' | 'duplicate'
  inserted: BackupManifest['counts']
  skipped: BackupManifest['counts']
  wiped: BackupManifest['counts']
  files: { uploaded: number; skipped: number; wiped: number }
  warnings: string[]
  duration_ms: number
}

// Local persistence of past backups so the user can see history without us
// having to remember it server-side. Kept tiny — last 10 entries.
export type BackupHistoryEntry = {
  id: string
  created_at: string
  duration_ms: number
  size_bytes: number
  counts: BackupManifest['counts']
}

const HISTORY_KEY = 'foldex.backups'
const HISTORY_MAX = 10
const BACKUP_REQUEST_TIMEOUT_MS = 30 * 60_000
const DOWNLOAD_STATUS_INITIAL_POLL_MS = 1_000
const DOWNLOAD_STATUS_MAX_POLL_MS = 10_000

function removeBackupHistory() {
  try { localStorage.removeItem(HISTORY_KEY) } catch { /* storage unavailable */ }
}

function replaceBackupHistory(history: BackupHistoryEntry[]) {
  try { localStorage.setItem(HISTORY_KEY, JSON.stringify(history)) } catch { /* storage unavailable */ }
}

export function readBackupHistory(): BackupHistoryEntry[] {
  if (typeof localStorage === 'undefined') return []
  try {
    const raw = localStorage.getItem(HISTORY_KEY)
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) {
      removeBackupHistory()
      return []
    }
    const history = parsed.map(normalizeBackupHistoryEntry)
      .filter((entry): entry is BackupHistoryEntry => entry !== null)
      .slice(0, HISTORY_MAX)
    const normalized = history.some((entry, index) => entry !== parsed[index])
    if (history.length !== parsed.length || normalized) {
      if (history.length === 0) removeBackupHistory()
      else replaceBackupHistory(history)
    }
    return history
  } catch {
    removeBackupHistory()
    return []
  }
}

export function appendBackupHistory(entry: BackupHistoryEntry) {
  if (typeof localStorage === 'undefined') return
  const next = [entry, ...readBackupHistory()].slice(0, HISTORY_MAX)
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(next))
  } catch { /* quota exceeded or private browsing; backup download still succeeds */ }
}

function isBackupHistoryEntry(value: unknown): value is BackupHistoryEntry {
  if (!isRecord(value) || !isRecord(value.counts)) return false
  const counts = value.counts
  return (
    typeof value.id === 'string' && value.id.length > 0 &&
    typeof value.created_at === 'string' && !Number.isNaN(Date.parse(value.created_at)) &&
    isNonNegativeInteger(value.duration_ms) &&
    isNonNegativeInteger(value.size_bytes) &&
    isNonNegativeInteger(counts.links) &&
    isNonNegativeInteger(counts.notes) &&
    isNonNegativeInteger(counts.tags) &&
    isNonNegativeInteger(counts.folders) &&
    isNonNegativeInteger(counts.link_tags) &&
    isNonNegativeInteger(counts.click_logs) &&
    isNonNegativeInteger(counts.files) &&
    isNonNegativeInteger(counts.file_bytes)
  )
}

function normalizeBackupHistoryEntry(value: unknown): BackupHistoryEntry | null {
  if (!isRecord(value) || !isRecord(value.counts)) return null
  const candidate = value.counts.notes === undefined
    ? { ...value, counts: { ...value.counts, notes: 0 } }
    : value
  return isBackupHistoryEntry(candidate) ? candidate : null
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isNonNegativeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

export async function generateBackup(): Promise<BackupHistoryEntry> {
  const picker = (window as Window & { showSaveFilePicker?: SaveFilePicker }).showSaveFilePicker
  if (picker) return streamBackupToFile(picker)
  return navigateBackupDownload()
}

type BackupWriter = {
  write: (chunk: Uint8Array) => Promise<void>
  close: () => Promise<void>
  abort: (reason?: unknown) => Promise<void>
}

type SaveFilePicker = (options: {
  suggestedName: string
  types: { description: string; accept: Record<string, string[]> }[]
}) => Promise<{ createWritable: () => Promise<{ getWriter: () => BackupWriter }> }>

async function streamBackupToFile(picker: SaveFilePicker): Promise<BackupHistoryEntry> {
  const suggestedName = backupFilename(new Date().toISOString())
  const handle = await picker.call(window, {
    suggestedName,
    types: [{ description: 'ZIP archive', accept: { 'application/zip': ['.zip'] } }],
  })
  const writer = (await handle.createWritable()).getWriter()
  const t0 = performance.now()
  let reader: ReadableStreamDefaultReader<Uint8Array> | null = null
  let size = 0
  try {
    const response = await authenticatedFetch('/api/backup', {
      method: 'POST',
      signal: AbortSignal.timeout(BACKUP_REQUEST_TIMEOUT_MS),
    })
    if (!response.ok) throw await backupResponseError(response)
    if (!response.body) throw new Error('backup response is not streamable')
    reader = response.body.getReader()
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      size += value.byteLength
      await writer.write(value)
    }
    await writer.close()
    return recordBackup(response.headers, size, t0)
  } catch (error) {
    await Promise.allSettled([
      reader?.cancel(error),
      writer.abort(error),
    ])
    throw error
  }
}

async function backupResponseError(response: Response): Promise<Error> {
  let data: unknown
  try { data = await response.json() } catch { data = undefined }
  const message = (data as { error?: { message?: string } } | undefined)?.error?.message
    ?? `backup request failed (${response.status})`
  return Object.assign(new Error(message), { response: { status: response.status, data } })
}

type BackupDownloadTicket = {
  id: string
  download_url: string
  status_url: string
  filename: string
  created_at: string
  expires_at: string
}

type BackupDownloadStatus = {
  id: string
  state: 'pending' | 'running' | 'complete' | 'failed'
  created_at: string
  duration_ms: number
  size_bytes: number
  counts: BackupManifest['counts']
  error?: { code: string; message: string }
}

async function navigateBackupDownload(): Promise<BackupHistoryEntry> {
  const { data: ticket } = await http.post<BackupDownloadTicket>('/api/backup/download')
  const downloadURL = sameOriginURL(ticket.download_url)
  const statusURL = sameOriginURL(ticket.status_url)
  const a = document.createElement('a')
  a.href = downloadURL.href
  a.download = ticket.filename
  a.referrerPolicy = 'no-referrer'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)

  const deadline = performance.now() + BACKUP_REQUEST_TIMEOUT_MS
  let pollDelay = DOWNLOAD_STATUS_INITIAL_POLL_MS
  for (;;) {
    if (performance.now() >= deadline) throw new Error('backup download timed out')
    const { data: status } = await http.get<BackupDownloadStatus>(statusURL.pathname + statusURL.search)
    if (status.state === 'complete') {
      const entry: BackupHistoryEntry = {
        id: status.id,
        created_at: status.created_at,
        duration_ms: status.duration_ms,
        size_bytes: status.size_bytes,
        counts: status.counts,
      }
      appendBackupHistory(entry)
      return entry
    }
    if (status.state === 'failed') {
      const data = { error: status.error ?? { code: 'export_failed', message: 'failed to produce backup' } }
      throw Object.assign(new Error(data.error.message), { response: { status: 409, data } })
    }
    const remaining = deadline - performance.now()
    if (remaining <= 0) throw new Error('backup download timed out')
    await new Promise((resolve) => setTimeout(resolve, Math.min(pollDelay, remaining)))
    pollDelay = Math.min(pollDelay * 2, DOWNLOAD_STATUS_MAX_POLL_MS)
  }
}

function sameOriginURL(value: string): URL {
  const url = new URL(value, window.location.href)
  if (url.origin !== window.location.origin) throw new Error('backup download URL must be same-origin')
  return url
}

type HeaderSource = Record<string, unknown> | { get(name: string): unknown }

function recordBackup(
  headers: HeaderSource,
  size: number,
  startedAt: number,
  manifest?: BackupManifest | null,
): BackupHistoryEntry {
  const created_at = manifest?.created_at ?? createdAtFromHeaders(headers) ?? new Date().toISOString()
  const entry: BackupHistoryEntry = {
    id: created_at,
    created_at,
    duration_ms: Math.round(performance.now() - startedAt),
    size_bytes: size,
    counts: manifest?.counts ?? countsFromHeaders(headers) ?? emptyBackupCounts(),
  }
  appendBackupHistory(entry)
  return entry
}

function emptyBackupCounts(): BackupManifest['counts'] {
  return { links: 0, notes: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 }
}

function backupFilename(createdAt: string): string {
  return `foldex-backup-${createdAt.replace(/[-:]/g, '').replace(/\.\d+Z?$/, 'Z')}.zip`
}

export async function validateBackup(file: File, signal?: AbortSignal): Promise<BackupValidation> {
  const fd = new FormData()
  fd.append('file', file)
  const { data } = await http.post<BackupValidation>('/api/backup/validate', fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
    timeout: BACKUP_REQUEST_TIMEOUT_MS,
    signal,
  })
  return data
}

export async function restoreBackup(
  file: File,
  mode: 'wipe' | 'skip' | 'duplicate',
): Promise<RestoreReport> {
  const fd = new FormData()
  fd.append('file', file)
  const { data } = await http.post<RestoreReport>(`/api/backup/restore?mode=${mode}`, fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
    timeout: BACKUP_REQUEST_TIMEOUT_MS,
  })
  return data
}

// useRestoreBackup mirrors useApplyImport — restore mutates every domain
// table, so the Home view must invalidate links/folders/tags/stats post-restore.
export function useRestoreBackup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (args: { file: File; mode: 'wipe' | 'skip' | 'duplicate' }) =>
      restoreBackup(args.file, args.mode),
    onSettled: () => {
      for (const queryKey of ['links', 'entries', 'folders', 'tags', 'stats']) {
        void Promise.resolve(qc.invalidateQueries({ queryKey: [queryKey] })).catch(() => undefined)
      }
      invalidateEntryCounts(qc)
    },
  })
}

function headerGet(headers: HeaderSource | undefined, name: string): string | undefined {
  if (!headers) return undefined
  if ('get' in headers && typeof headers.get === 'function') {
    const value = headers.get(name)
    if (value != null && value !== '') return String(value)
  }
  const lower = name.toLowerCase()
  for (const [k, v] of Object.entries(headers)) {
    if (k.toLowerCase() === lower && v != null && v !== '') return String(v)
  }
  return undefined
}

/** Prefer X-Foldex-Backup-Counts-* so the client never double-buffers the zip. */
export function countsFromHeaders(headers: HeaderSource | undefined): BackupManifest['counts'] | null {
  const links = headerGet(headers, 'x-foldex-backup-counts-links')
  if (links == null) return null
  const n = (k: string) => {
    const v = headerGet(headers, k)
    if (v == null) return 0
    const parsed = Number(v)
    return Number.isFinite(parsed) ? parsed : 0
  }
  return {
    links: Number(links) || 0,
    notes: n('x-foldex-backup-counts-notes'),
    tags: n('x-foldex-backup-counts-tags'),
    folders: n('x-foldex-backup-counts-folders'),
    link_tags: n('x-foldex-backup-counts-link-tags'),
    click_logs: n('x-foldex-backup-counts-click-logs'),
    files: n('x-foldex-backup-counts-files'),
    file_bytes: n('x-foldex-backup-counts-file-bytes'),
  }
}

function createdAtFromHeaders(headers: HeaderSource | undefined): string | null {
  const filename = headerGet(headers, 'x-foldex-backup-filename')
  if (!filename) return null
  // foldex-backup-20060102T150405Z.zip
  const m = filename.match(/foldex-backup-(\d{8}T\d{6}Z)\.zip$/i)
  if (!m) return null
  const raw = m[1]!
  // 20060102T150405Z → 2006-01-02T15:04:05Z
  return `${raw.slice(0, 4)}-${raw.slice(4, 6)}-${raw.slice(6, 8)}T${raw.slice(9, 11)}:${raw.slice(11, 13)}:${raw.slice(13, 15)}Z`
}

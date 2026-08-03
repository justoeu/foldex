import { useMutation, useQueryClient } from '@tanstack/react-query'
import { http } from './client'

export type BackupManifest = {
  kind: string
  version: string
  schema_version: number
  created_at: string
  foldex_version?: string
  counts: {
    links: number
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

export function readBackupHistory(): BackupHistoryEntry[] {
  if (typeof localStorage === 'undefined') return []
  try {
    const raw = localStorage.getItem(HISTORY_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

export function appendBackupHistory(entry: BackupHistoryEntry) {
  if (typeof localStorage === 'undefined') return
  const next = [entry, ...readBackupHistory()].slice(0, HISTORY_MAX)
  localStorage.setItem(HISTORY_KEY, JSON.stringify(next))
}

// Generate triggers download via blob (so we capture timing + size client-side
// for the history). Returns the recorded entry.
export async function generateBackup(): Promise<BackupHistoryEntry> {
  const t0 = performance.now()
  const res = await http.post('/api/backup', null, { responseType: 'blob' })
  const blob = res.data as Blob
  // Prefer response headers (no second full-zip buffer). Fall back to a
  // slice-based EOCD walk that never holds the whole zip as Uint8Array.
  const fromHeaders = countsFromHeaders(res.headers as Record<string, unknown>)
  const manifest = fromHeaders
    ? ({
        kind: 'foldex.backup',
        version: '1.0',
        schema_version: 0,
        created_at: createdAtFromHeaders(res.headers as Record<string, unknown>) ?? new Date().toISOString(),
        counts: fromHeaders,
        checksums: {},
      } satisfies BackupManifest)
    : await extractManifestFromZip(blob)
  const duration_ms = Math.round(performance.now() - t0)
  const id = manifest?.created_at ?? new Date().toISOString()
  const entry: BackupHistoryEntry = {
    id,
    created_at: manifest?.created_at ?? new Date().toISOString(),
    duration_ms,
    size_bytes: blob.size,
    counts:
      manifest?.counts ?? {
        links: 0, tags: 0, folders: 0, link_tags: 0,
        click_logs: 0, files: 0, file_bytes: 0,
      },
  }
  appendBackupHistory(entry)

  // Trigger the download via an object URL so the browser saves the file.
  const stamp = (manifest?.created_at ?? new Date().toISOString()).replace(/[-:]/g, '').replace(/\.\d+Z?$/, 'Z')
  const filename = `foldex-backup-${stamp}.zip`
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
  return entry
}

export async function validateBackup(file: File): Promise<BackupValidation> {
  const fd = new FormData()
  fd.append('file', file)
  const { data } = await http.post<BackupValidation>('/api/backup/validate', fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
  })
  return data
}

export async function restoreBackup(file: File, mode: 'wipe' | 'skip' | 'duplicate'): Promise<RestoreReport> {
  const fd = new FormData()
  fd.append('file', file)
  const { data } = await http.post<RestoreReport>(`/api/backup/restore?mode=${mode}`, fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
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
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['links'] })
      qc.invalidateQueries({ queryKey: ['entries'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
      qc.invalidateQueries({ queryKey: ['tags'] })
      qc.invalidateQueries({ queryKey: ['stats'] })
    },
  })
}

function headerGet(headers: Record<string, unknown> | undefined, name: string): string | undefined {
  if (!headers) return undefined
  const lower = name.toLowerCase()
  for (const [k, v] of Object.entries(headers)) {
    if (k.toLowerCase() === lower && v != null && v !== '') return String(v)
  }
  // axios may expose a get() on AxiosHeaders
  const maybeGet = (headers as { get?: (k: string) => unknown }).get
  if (typeof maybeGet === 'function') {
    const v = maybeGet.call(headers, name)
    if (v != null && v !== '') return String(v)
  }
  return undefined
}

/** Prefer X-Foldex-Backup-Counts-* so the client never double-buffers the zip. */
export function countsFromHeaders(headers: Record<string, unknown> | undefined): BackupManifest['counts'] | null {
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
    tags: n('x-foldex-backup-counts-tags'),
    folders: n('x-foldex-backup-counts-folders'),
    link_tags: n('x-foldex-backup-counts-link-tags'),
    click_logs: n('x-foldex-backup-counts-click-logs'),
    files: n('x-foldex-backup-counts-files'),
    file_bytes: n('x-foldex-backup-counts-file-bytes'),
  }
}

function createdAtFromHeaders(headers: Record<string, unknown> | undefined): string | null {
  const filename = headerGet(headers, 'x-foldex-backup-filename')
  if (!filename) return null
  // foldex-backup-20060102T150405Z.zip
  const m = filename.match(/foldex-backup-(\d{8}T\d{6}Z)\.zip$/i)
  if (!m) return null
  const raw = m[1]!
  // 20060102T150405Z → 2006-01-02T15:04:05Z
  return `${raw.slice(0, 4)}-${raw.slice(4, 6)}-${raw.slice(6, 8)}T${raw.slice(9, 11)}:${raw.slice(11, 13)}:${raw.slice(13, 15)}Z`
}

// ────────────────────────────────────────────────────────────────────────────
// Tiny ZIP central-directory walker — finds manifest.json + parses it.
// Backend writes manifest.json with Method=Store (uncompressed) so we don't
// need an inflater here. Uses blob.slice only — never holds a full-zip
// arrayBuffer alongside the Blob (LEAK-HYD-009).

export async function extractManifestFromZip(blob: Blob): Promise<BackupManifest | null> {
  if (blob.size < 22) return null
  // EOCD is in the last 22..65557 bytes (comment up to 64 KiB).
  const eocdSearch = Math.min(blob.size, 22 + 0xffff)
  const tailStart = blob.size - eocdSearch
  const tail = new Uint8Array(await blob.slice(tailStart).arrayBuffer())
  let eocdRel = -1
  for (let i = tail.length - 22; i >= 0; i--) {
    if (tail[i] === 0x50 && tail[i + 1] === 0x4b && tail[i + 2] === 0x05 && tail[i + 3] === 0x06) {
      eocdRel = i
      break
    }
  }
  if (eocdRel < 0) return null
  const tdv = new DataView(tail.buffer, tail.byteOffset, tail.byteLength)
  const totalEntries = tdv.getUint16(eocdRel + 10, true)
  const cdSize = tdv.getUint32(eocdRel + 12, true)
  const cdOffset = tdv.getUint32(eocdRel + 16, true)
  if (cdSize === 0 || cdOffset + cdSize > blob.size) return null

  const cdBuf = new Uint8Array(await blob.slice(cdOffset, cdOffset + cdSize).arrayBuffer())
  const cdDv = new DataView(cdBuf.buffer, cdBuf.byteOffset, cdBuf.byteLength)
  let pos = 0
  for (let i = 0; i < totalEntries && pos + 46 <= cdBuf.length; i++) {
    if (cdDv.getUint32(pos, true) !== 0x02014b50) return null
    const compression = cdDv.getUint16(pos + 10, true)
    const compSize = cdDv.getUint32(pos + 20, true)
    const nameLen = cdDv.getUint16(pos + 28, true)
    const extraLen = cdDv.getUint16(pos + 30, true)
    const commentLen = cdDv.getUint16(pos + 32, true)
    const localHdrOff = cdDv.getUint32(pos + 42, true)
    if (pos + 46 + nameLen > cdBuf.length) return null
    const name = new TextDecoder().decode(cdBuf.subarray(pos + 46, pos + 46 + nameLen))
    if (name === 'manifest.json' && compression === 0) {
      // Local header is 30 bytes + name + extra; probe just enough for those.
      const lhCap = Math.min(blob.size - localHdrOff, 30 + nameLen + 2048)
      const lhProbe = new Uint8Array(await blob.slice(localHdrOff, localHdrOff + lhCap).arrayBuffer())
      if (lhProbe.length < 30) return null
      const lhDv = new DataView(lhProbe.buffer, lhProbe.byteOffset, lhProbe.byteLength)
      const lhNameLen = lhDv.getUint16(26, true)
      const lhExtraLen = lhDv.getUint16(28, true)
      const dataStart = localHdrOff + 30 + lhNameLen + lhExtraLen
      if (dataStart + compSize > blob.size) return null
      const data = new Uint8Array(await blob.slice(dataStart, dataStart + compSize).arrayBuffer())
      try {
        return JSON.parse(new TextDecoder().decode(data)) as BackupManifest
      } catch {
        return null
      }
    }
    pos += 46 + nameLen + extraLen + commentLen
  }
  return null
}

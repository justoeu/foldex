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
  const arr = new Uint8Array(await blob.arrayBuffer())
  // Extract the manifest entry from the zip to record counts. We do this
  // client-side via a tiny zip-end-of-central-directory walker — cheap and
  // avoids pulling a dependency.
  const manifest = extractManifestFromZip(arr)
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

// ────────────────────────────────────────────────────────────────────────────
// Tiny ZIP central-directory walker — finds manifest.json + parses it.
// Backend writes manifest.json with Method=Store (uncompressed) so we don't
// need an inflater here.

function extractManifestFromZip(buf: Uint8Array): BackupManifest | null {
  let eocd = -1
  for (let i = buf.length - 22; i >= Math.max(0, buf.length - 65557); i--) {
    if (buf[i] === 0x50 && buf[i + 1] === 0x4b && buf[i + 2] === 0x05 && buf[i + 3] === 0x06) {
      eocd = i
      break
    }
  }
  if (eocd < 0) return null
  const dv = new DataView(buf.buffer, buf.byteOffset, buf.byteLength)
  const totalEntries = dv.getUint16(eocd + 10, true)
  const cdSize = dv.getUint32(eocd + 12, true)
  const cdOffset = dv.getUint32(eocd + 16, true)
  let pos = cdOffset
  for (let i = 0; i < totalEntries && pos < cdOffset + cdSize; i++) {
    if (dv.getUint32(pos, true) !== 0x02014b50) return null
    const compression = dv.getUint16(pos + 10, true)
    const compSize = dv.getUint32(pos + 20, true)
    const nameLen = dv.getUint16(pos + 28, true)
    const extraLen = dv.getUint16(pos + 30, true)
    const commentLen = dv.getUint16(pos + 32, true)
    const localHdrOff = dv.getUint32(pos + 42, true)
    const name = new TextDecoder().decode(buf.subarray(pos + 46, pos + 46 + nameLen))
    if (name === 'manifest.json' && compression === 0) {
      const lh = localHdrOff
      const lhNameLen = dv.getUint16(lh + 26, true)
      const lhExtraLen = dv.getUint16(lh + 28, true)
      const dataStart = lh + 30 + lhNameLen + lhExtraLen
      const data = buf.subarray(dataStart, dataStart + compSize)
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

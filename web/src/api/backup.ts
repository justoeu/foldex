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
const BACKUP_REQUEST_TIMEOUT_MS = 30 * 60_000

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
    const history = parsed.filter(isBackupHistoryEntry).slice(0, HISTORY_MAX)
    if (history.length !== parsed.length) {
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
    isNonNegativeInteger(counts.tags) &&
    isNonNegativeInteger(counts.folders) &&
    isNonNegativeInteger(counts.link_tags) &&
    isNonNegativeInteger(counts.click_logs) &&
    isNonNegativeInteger(counts.files) &&
    isNonNegativeInteger(counts.file_bytes)
  )
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isNonNegativeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

// Generate triggers download via blob (so we capture timing + size client-side
// for the history). Returns the recorded entry.
export async function generateBackup(): Promise<BackupHistoryEntry> {
  const t0 = performance.now()
  const res = await http.post('/api/backup', null, {
    responseType: 'blob',
    timeout: BACKUP_REQUEST_TIMEOUT_MS,
  })
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
    timeout: BACKUP_REQUEST_TIMEOUT_MS,
  })
  return data
}

export async function restoreBackup(file: File, mode: 'wipe' | 'skip' | 'duplicate'): Promise<RestoreReport> {
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
    onSuccess: () => Promise.all([
      qc.invalidateQueries({ queryKey: ['links'] }),
      qc.invalidateQueries({ queryKey: ['entries'] }),
      qc.invalidateQueries({ queryKey: ['folders'] }),
      qc.invalidateQueries({ queryKey: ['tags'] }),
      qc.invalidateQueries({ queryKey: ['stats'] }),
    ]),
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

// The fallback parser reads only bounded slices. The backend stores the
// manifest without compression, so no ZIP inflater or full-blob copy is needed.
const EOCD_SIGNATURE = 0x06054b50
const CENTRAL_SIGNATURE = 0x02014b50
const LOCAL_SIGNATURE = 0x04034b50
const MAX_EOCD_BYTES = 22 + 0xffff
const MAX_CENTRAL_DIRECTORY_BYTES = 32 << 20
const MAX_MANIFEST_BYTES = 32 << 20
const ZIP32_SENTINEL = 0xffffffff
const ZIP16_SENTINEL = 0xffff
const ENCRYPTED_FLAG = 1
const DATA_DESCRIPTOR_FLAG = 1 << 3
const textDecoder = new TextDecoder()

type ByteReader = { bytes: Uint8Array; view: DataView }
type EndOfCentralDirectory = { entries: number; offset: number; size: number }
type CentralEntry = {
  flags: number
  compression: number
  compressedSize: number
  uncompressedSize: number
  localOffset: number
  name: string
  nextOffset: number
}
type LocalHeader = {
  flags: number
  compression: number
  compressedSize: number
  uncompressedSize: number
  nameLength: number
  extraLength: number
}

function byteReader(bytes: Uint8Array): ByteReader {
  return { bytes, view: new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength) }
}

function boundedEnd(start: number, length: number, total: number, maxLength = total): number | null {
  if (!Number.isSafeInteger(start) || !Number.isSafeInteger(length)) return null
  if (start < 0 || length < 0 || length > maxLength) return null
  const end = start + length
  return Number.isSafeInteger(end) && end >= start && end <= total ? end : null
}

function readU16(reader: ByteReader, offset: number): number | null {
  return boundedEnd(offset, 2, reader.bytes.length) == null ? null : reader.view.getUint16(offset, true)
}

function readU32(reader: ByteReader, offset: number): number | null {
  return boundedEnd(offset, 4, reader.bytes.length) == null ? null : reader.view.getUint32(offset, true)
}

function allNumbers(values: (number | null)[]): values is number[] {
  return values.every((value) => value != null)
}

async function readBlobRange(blob: Blob, start: number, length: number, maxLength: number): Promise<ByteReader | null> {
  const end = boundedEnd(start, length, blob.size, maxLength)
  if (end == null) return null
  const bytes = new Uint8Array(await blob.slice(start, end).arrayBuffer())
  return bytes.length === length ? byteReader(bytes) : null
}

function parseEndRecord(reader: ByteReader, relativeOffset: number, absoluteOffset: number, blobSize: number): EndOfCentralDirectory | null {
  const fields = [
    readU16(reader, relativeOffset + 4),
    readU16(reader, relativeOffset + 6),
    readU16(reader, relativeOffset + 8),
    readU16(reader, relativeOffset + 10),
    readU32(reader, relativeOffset + 12),
    readU32(reader, relativeOffset + 16),
    readU16(reader, relativeOffset + 20),
  ]
  if (!allNumbers(fields)) return null
  const [disk, centralDisk, diskEntries, entries, size, offset, commentLength] = fields
  if (disk !== 0) return null
  if (centralDisk !== 0) return null
  if (diskEntries !== entries) return null
  if (entries === ZIP16_SENTINEL) return null
  if ([size, offset].includes(ZIP32_SENTINEL)) return null
  if (size === 0) return null
  if (boundedEnd(absoluteOffset, 22 + commentLength, blobSize) !== blobSize) return null
  if (boundedEnd(offset, size, absoluteOffset, MAX_CENTRAL_DIRECTORY_BYTES) == null) return null
  return { entries, offset, size }
}

async function readEndOfCentralDirectory(blob: Blob): Promise<EndOfCentralDirectory | null> {
  if (blob.size < 22) return null
  const tailLength = Math.min(blob.size, MAX_EOCD_BYTES)
  const tailStart = blob.size - tailLength
  const tail = await readBlobRange(blob, tailStart, tailLength, MAX_EOCD_BYTES)
  if (!tail) return null
  for (let pos = tailLength - 22; pos >= 0; pos--) {
    if (readU32(tail, pos) !== EOCD_SIGNATURE) continue
    const record = parseEndRecord(tail, pos, tailStart + pos, blob.size)
    if (record) return record
  }
  return null
}

function readCentralEntry(reader: ByteReader, offset: number): CentralEntry | null {
  if (readU32(reader, offset) !== CENTRAL_SIGNATURE) return null
  const fields = [
    readU16(reader, offset + 8),
    readU16(reader, offset + 10),
    readU32(reader, offset + 20),
    readU32(reader, offset + 24),
    readU16(reader, offset + 28),
    readU16(reader, offset + 30),
    readU16(reader, offset + 32),
    readU32(reader, offset + 42),
  ]
  if (!allNumbers(fields)) return null
  const [flags, compression, compressedSize, uncompressedSize, nameLength, extraLength, commentLength, localOffset] = fields
  if ((flags & ENCRYPTED_FLAG) !== 0) return null
  if ([compressedSize, uncompressedSize, localOffset].includes(ZIP32_SENTINEL)) return null
  const nameStart = offset + 46
  const nameEnd = boundedEnd(nameStart, nameLength, reader.bytes.length)
  if (nameEnd == null) return null
  const nextOffset = boundedEnd(nameEnd, extraLength + commentLength, reader.bytes.length)
  if (nextOffset == null) return null
  const name = textDecoder.decode(reader.bytes.subarray(nameStart, nameEnd))
  return { flags, compression, compressedSize, uncompressedSize, localOffset, name, nextOffset }
}

async function findManifestEntry(blob: Blob, eocd: EndOfCentralDirectory): Promise<CentralEntry | null> {
  const central = await readBlobRange(blob, eocd.offset, eocd.size, MAX_CENTRAL_DIRECTORY_BYTES)
  if (!central) return null
  let offset = 0
  let manifest: CentralEntry | null = null
  for (let index = 0; index < eocd.entries; index++) {
    const entry = readCentralEntry(central, offset)
    if (!entry) return null
    if (entry.name === 'manifest.json' && manifest) return null
    if (entry.name === 'manifest.json') manifest = entry
    offset = entry.nextOffset
  }
  return offset === central.bytes.length ? manifest : null
}

function readLocalHeader(reader: ByteReader): LocalHeader | null {
  if (readU32(reader, 0) !== LOCAL_SIGNATURE) return null
  const fields = [
    readU16(reader, 6),
    readU16(reader, 8),
    readU32(reader, 18),
    readU32(reader, 22),
    readU16(reader, 26),
    readU16(reader, 28),
  ]
  if (!allNumbers(fields)) return null
  const [flags, compression, compressedSize, uncompressedSize, nameLength, extraLength] = fields
  return { flags, compression, compressedSize, uncompressedSize, nameLength, extraLength }
}

function localHeaderMatches(entry: CentralEntry, local: LocalHeader, name: string): boolean {
  if (name !== entry.name) return false
  if (local.flags !== entry.flags) return false
  if (local.compression !== entry.compression) return false
  if ((local.flags & DATA_DESCRIPTOR_FLAG) !== 0) return true
  if (local.compressedSize !== entry.compressedSize) return false
  return local.uncompressedSize === entry.uncompressedSize
}

function isStringRecord(value: unknown): value is Record<string, string> {
  return isRecord(value) && Object.values(value).every((item) => typeof item === 'string')
}

function isBackupCounts(value: unknown): value is BackupManifest['counts'] {
  if (!isRecord(value)) return false
  const fields = ['links', 'tags', 'folders', 'link_tags', 'click_logs', 'files', 'file_bytes'] as const
  return fields.every((field) => isNonNegativeInteger(value[field]))
}

function isBackupManifest(value: unknown): value is BackupManifest {
  if (!isRecord(value)) return false
  if (typeof value.kind !== 'string' || typeof value.version !== 'string') return false
  if (!isNonNegativeInteger(value.schema_version) || typeof value.created_at !== 'string') return false
  if (value.foldex_version != null && typeof value.foldex_version !== 'string') return false
  return isBackupCounts(value.counts) && isStringRecord(value.checksums)
}

function parseManifest(bytes: Uint8Array): BackupManifest | null {
  try {
    const parsed: unknown = JSON.parse(textDecoder.decode(bytes))
    return isBackupManifest(parsed) ? parsed : null
  } catch {
    return null
  }
}

async function readStoredManifest(blob: Blob, entry: CentralEntry): Promise<BackupManifest | null> {
  if (entry.compression !== 0 || entry.compressedSize !== entry.uncompressedSize) return null
  if (entry.compressedSize > MAX_MANIFEST_BYTES) return null
  const localReader = await readBlobRange(blob, entry.localOffset, 30, 30)
  if (!localReader) return null
  const local = readLocalHeader(localReader)
  if (!local) return null
  if ((local.flags & ENCRYPTED_FLAG) !== 0) return null
  const nameStart = entry.localOffset + 30
  const localName = await readBlobRange(blob, nameStart, local.nameLength, ZIP16_SENTINEL)
  if (!localName) return null
  if (!localHeaderMatches(entry, local, textDecoder.decode(localName.bytes))) return null
  const dataStart = boundedEnd(nameStart, local.nameLength + local.extraLength, blob.size)
  if (dataStart == null) return null
  const data = await readBlobRange(blob, dataStart, entry.compressedSize, MAX_MANIFEST_BYTES)
  return data ? parseManifest(data.bytes) : null
}

export async function extractManifestFromZip(blob: Blob): Promise<BackupManifest | null> {
  const eocd = await readEndOfCentralDirectory(blob)
  if (!eocd) return null
  const entry = await findManifestEntry(blob, eocd)
  return entry ? readStoredManifest(blob, entry) : null
}

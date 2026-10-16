import { downloadBackupArtifact } from '../../api/admin'
import { streamResponseToPickedFile, type SaveFilePicker } from '../../lib/streamResponseToPickedFile'

/**
 * A dump can be gigabytes. Buffering one as a Blob holds the whole thing in
 * the tab's memory before a single byte reaches disk — it works for a small
 * instance and freezes or crashes the tab for a real one, which is exactly the
 * instance whose backup matters most.
 *
 * So the artifact streams straight to the file the operator picked, chunk by
 * chunk, and the Blob path survives only as the fallback for browsers without
 * the File System Access API (Firefox, Safari). Same split, and the same
 * reasoning, as `generateBackup` in api/backup.ts.
 */
const ARTIFACT_TIMEOUT_MS = 30 * 60_000

export type BackupDownloadDeps = {
  downloadArtifact?: typeof downloadBackupArtifact
  picker?: SaveFilePicker
  createObjectURL?: (blob: Blob) => string
  revokeObjectURL?: (url: string) => void
  doc?: Document
}

/** The last path segment of an object key — what the row already shows. */
export function artifactFilename(key: string): string {
  const name = key.split('/').findLast(Boolean) ?? ''
  return name === '' ? 'foldex-backup.age' : name
}

/** Always `.age`: a file called `.dump` that age wrote fails in pg_restore
 *  looking like a corrupt backup rather than an undecrypted one. */
function encryptedName(filename: string): string {
  return filename.endsWith('.age') ? filename : `${filename}.age`
}

/**
 * Saves the artifact the agent re-encrypted (ADR-48), fetched via
 * downloadBackupArtifact in api/admin (body-carried password, INV-036).
 */
export async function saveBackupArtifact(
  runID: number,
  password: string,
  filename: string,
  deps: BackupDownloadDeps = {},
): Promise<void> {
  const picker = 'picker' in deps
    ? deps.picker
    : (window as Window & { showSaveFilePicker?: SaveFilePicker }).showSaveFilePicker
  const name = encryptedName(filename)
  return picker
    ? streamArtifactToFile(picker, runID, password, name, deps)
    : bufferArtifactToBlob(runID, password, name, deps)
}

function requestArtifact(
  runID: number,
  password: string,
  deps: BackupDownloadDeps,
): Promise<Response> {
  const downloadArtifact = deps.downloadArtifact ?? downloadBackupArtifact
  return downloadArtifact(runID, password, AbortSignal.timeout(ARTIFACT_TIMEOUT_MS))
}

async function artifactError(response: Response): Promise<Error> {
  let data: unknown
  try { data = await response.json() } catch { data = undefined }
  const envelope = (data as { error?: { message?: string; code?: string } } | undefined)?.error
  return new Error(envelope?.message ?? envelope?.code ?? `HTTP ${response.status}`)
}

async function streamArtifactToFile(
  picker: SaveFilePicker,
  runID: number,
  password: string,
  name: string,
  deps: BackupDownloadDeps,
): Promise<void> {
  /* The shared pump opens the picker before the request and aborts both
     sides on failure: a half-written file left on disk would look like a
     backup, and age's chunk authentication makes a truncated one unopenable
     rather than silently short — but the file should not be there at all. */
  await streamResponseToPickedFile({
    picker,
    suggestedName: name,
    types: [{ description: 'age-encrypted archive', accept: { 'application/octet-stream': ['.age'] } }],
    fetchResponse: () => requestArtifact(runID, password, deps),
    responseError: artifactError,
    notStreamableMessage: 'artifact response is not streamable',
  })
}

/**
 * The fallback: no File System Access API, so the whole artifact lands in
 * memory before it lands on disk. Kept because Firefox and Safari have no
 * alternative, and a download that is heavy beats one that is impossible.
 */
async function bufferArtifactToBlob(
  runID: number,
  password: string,
  name: string,
  deps: BackupDownloadDeps,
): Promise<void> {
  const createObjectURL = deps.createObjectURL ?? ((b: Blob) => URL.createObjectURL(b))
  const revokeObjectURL = deps.revokeObjectURL ?? ((u: string) => URL.revokeObjectURL(u))
  const doc = deps.doc ?? document

  const response = await requestArtifact(runID, password, deps)
  if (!response.ok) throw await artifactError(response)
  const url = createObjectURL(await response.blob())
  try {
    const a = doc.createElement('a')
    a.href = url
    a.download = name
    doc.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    // Leaking the URL pins the whole dump in memory for the life of the document.
    revokeObjectURL(url)
  }
}

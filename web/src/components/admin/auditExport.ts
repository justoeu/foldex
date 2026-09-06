import { exportAuditCsv, type AuditQuery } from '../../api/admin'

export type AuditExportDeps = {
  exportCsv?: (q: AuditQuery) => Promise<Blob>
  createObjectURL?: (blob: Blob) => string
  revokeObjectURL?: (url: string) => void
  now?: () => Date
  doc?: Document
}

/**
 * Saves the CSV the server streamed.
 *
 * The blob is fetched through the axios client (cookies and the CSRF header)
 * and handed to the browser through an object URL. The URL is revoked in a
 * `finally`, because leaking one pins the whole file in memory for the life of
 * the document — and this file is deliberately allowed to be large.
 */
export async function downloadAuditCsv(filter: AuditQuery, deps: AuditExportDeps = {}): Promise<void> {
  const exportCsv = deps.exportCsv ?? exportAuditCsv
  const createObjectURL = deps.createObjectURL ?? ((b: Blob) => URL.createObjectURL(b))
  const revokeObjectURL = deps.revokeObjectURL ?? ((u: string) => URL.revokeObjectURL(u))
  const now = deps.now ?? (() => new Date())
  const doc = deps.doc ?? document

  const blob = await exportCsv(filter)
  const url = createObjectURL(blob)
  try {
    const a = doc.createElement('a')
    a.href = url
    a.download = `foldex-audit-${now().toISOString().slice(0, 10)}.csv`
    doc.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    revokeObjectURL(url)
  }
}

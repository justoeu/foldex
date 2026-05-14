import { useEffect, useRef, useState } from 'react'
import { Icon, I } from './icons'
import {
  generateBackup,
  readBackupHistory,
  type BackupHistoryEntry,
} from '../api/backup'
import { BackupRestoreDialog } from './BackupRestoreDialog'

type Props = {
  onRestored: () => void
}

export function BackupCard({ onRestored }: Props) {
  const [history, setHistory] = useState<BackupHistoryEntry[]>(() => readBackupHistory())
  const [generating, setGenerating] = useState(false)
  const [errMsg, setErrMsg] = useState<string | null>(null)
  const [restoreFile, setRestoreFile] = useState<File | null>(null)
  const [isDragging, setIsDragging] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    // Sync if another tab generated a backup.
    const onStorage = (e: StorageEvent) => {
      if (e.key === 'foldex.backups') setHistory(readBackupHistory())
    }
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [])

  const handleGenerate = async () => {
    setErrMsg(null)
    setGenerating(true)
    try {
      await generateBackup()
      setHistory(readBackupHistory())
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: { message?: string } } } })?.response?.data?.error?.message
        ?? (e as Error).message
        ?? 'falha ao gerar backup'
      setErrMsg(msg)
    } finally {
      setGenerating(false)
    }
  }

  const handleFile = (f: File | null) => {
    if (!f) return
    if (!f.name.toLowerCase().endsWith('.zip')) {
      setErrMsg('Arquivo precisa ser um .zip de backup do foldex.')
      return
    }
    setErrMsg(null)
    setRestoreFile(f)
  }

  return (
    <>
      <section className="fx-card">
        <div className="fx-card-body" style={{ gap: 14, padding: 18 }}>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
            <h3 className="fx-card-title" style={{ fontSize: 16, margin: 0 }}>
              💾 Backup completo
            </h3>
            <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)', letterSpacing: '0.08em', textTransform: 'uppercase' }}>
              DB + MinIO
            </span>
          </div>
          <div style={{ fontSize: 13, color: 'var(--fx-ink-3)' }}>
            Gera um ZIP com todas as tabelas (tags, pastas, links, M:N, click_log)
            e todos os arquivos do bucket (screenshots + imagens).
          </div>

          <button
            type="button"
            className="fx-cta fx-cta-fill"
            disabled={generating}
            onClick={handleGenerate}
            style={{ justifyContent: 'center' }}
          >
            {generating ? 'Gerando…' : 'Gerar backup completo'}
            <Icon d={I.upload} size={14} stroke={2} />
          </button>

          {errMsg && (
            <div style={{ fontSize: 12, color: 'var(--fx-danger)' }}>{errMsg}</div>
          )}

          <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)', marginTop: 6 }}>
            Restaurar de um backup
          </div>
          <div
            className={'fx-backup-dropzone' + (isDragging ? ' fx-backup-dropzone-drag' : '')}
            style={{
              border: '1.5px dashed var(--fx-border)',
              borderRadius: 12,
              padding: 22,
              textAlign: 'center',
              cursor: 'pointer',
              background: 'var(--fx-surface)',
            }}
            onDragOver={(e) => { e.preventDefault(); setIsDragging(true) }}
            onDragLeave={() => setIsDragging(false)}
            onDrop={(e) => {
              e.preventDefault()
              setIsDragging(false)
              handleFile(e.dataTransfer.files?.[0] ?? null)
            }}
            onClick={() => fileRef.current?.click()}
          >
            <Icon d={I.upload} size={22} />
            <div style={{ marginTop: 6, color: 'var(--fx-ink-3)', fontSize: 13 }}>
              Arraste o .zip ou clique pra escolher
            </div>
            <input
              ref={fileRef}
              type="file"
              hidden
              accept=".zip"
              onChange={(e) => handleFile(e.target.files?.[0] ?? null)}
            />
          </div>

          {history.length > 0 && (
            <>
              <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)', marginTop: 8 }}>
                Histórico
              </div>
              <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
                {history.map((h) => (
                  <li key={h.id} className="fx-backup-history-row">
                    <div style={{ fontSize: 12, color: 'var(--fx-ink)' }}>
                      {formatDate(h.created_at)}
                    </div>
                    <div style={{ fontSize: 11, color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)' }}>
                      {h.counts.files} files · {formatBytes(h.size_bytes)} · {formatDuration(h.duration_ms)} · {h.counts.links} links / {h.counts.tags} tags
                    </div>
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      </section>

      {restoreFile && (
        <BackupRestoreDialog
          file={restoreFile}
          onClose={() => setRestoreFile(null)}
          onRestored={() => {
            setRestoreFile(null)
            onRestored()
          }}
        />
      )}
    </>
  )
}

function formatDate(iso: string): string {
  try {
    const d = new Date(iso)
    return d.toLocaleString('pt-BR', { dateStyle: 'short', timeStyle: 'short' })
  } catch {
    return iso
  }
}

function formatBytes(b: number): string {
  if (b < 1024) return `${b} B`
  const units = ['KB', 'MB', 'GB']
  let n = b / 1024
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(n >= 10 ? 0 : 1)} ${units[i]}`
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

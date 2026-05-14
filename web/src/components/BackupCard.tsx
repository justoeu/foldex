import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
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
  const { t } = useTranslation()
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
        ?? t('backup.generate_failed')
      setErrMsg(msg)
    } finally {
      setGenerating(false)
    }
  }

  const handleFile = (f: File | null) => {
    if (!f) return
    if (!f.name.toLowerCase().endsWith('.zip')) {
      setErrMsg(t('backup.restore_file_invalid'))
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
              {t('backup.card_title')}
            </h3>
            <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)', letterSpacing: '0.08em', textTransform: 'uppercase' }}>
              {t('backup.card_kicker')}
            </span>
          </div>
          <div style={{ fontSize: 13, color: 'var(--fx-ink-3)' }}>
            {t('backup.card_body')}
          </div>

          <button
            type="button"
            className="fx-cta fx-cta-fill"
            disabled={generating}
            onClick={handleGenerate}
            style={{ justifyContent: 'center' }}
          >
            {generating ? t('backup.generating') : t('backup.generate_button')}
            <Icon d={I.upload} size={14} stroke={2} />
          </button>

          {errMsg && (
            <div style={{ fontSize: 12, color: 'var(--fx-danger)' }}>{errMsg}</div>
          )}

          <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)', marginTop: 6 }}>
            {t('backup.restore_section_title')}
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
              {t('backup.restore_dropzone')}
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
                {t('backup.history_section_title')}
              </div>
              <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
                {history.map((h) => (
                  <li key={h.id} className="fx-backup-history-row">
                    <div style={{ fontSize: 12, color: 'var(--fx-ink)' }}>
                      {formatDate(h.created_at)}
                    </div>
                    <div style={{ fontSize: 11, color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)' }}>
                      {t('backup.history_format_files', {
                        files: h.counts.files,
                        size: formatBytes(h.size_bytes),
                        duration: formatDuration(h.duration_ms, t),
                        links: h.counts.links,
                        tags: h.counts.tags,
                      })}
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
    return d.toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' })
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

function formatDuration(ms: number, t: TFunction): string {
  if (ms < 1000) return t('backup.duration_ms', { value: ms })
  return t('backup.duration_s', { value: (ms / 1000).toFixed(1) })
}

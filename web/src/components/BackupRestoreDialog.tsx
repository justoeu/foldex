import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import {
  useRestoreBackup,
  validateBackup,
  type BackupValidation,
  type RestoreReport,
} from '../api/backup'
import { apiErrorText } from '../lib/apiError'
import { ConflictModePicker, type ConflictMode } from './ConflictModePicker'

type Props = {
  file: File
  onClose: () => void
  onRestored: () => void
}

export function BackupRestoreDialog({ file, onClose, onRestored }: Props) {
  const { t } = useTranslation()
  const [validation, setValidation] = useState<BackupValidation | null>(null)
  const [loading, setLoading] = useState(true)
  const [errMsg, setErrMsg] = useState<string | null>(null)
  const [mode, setMode] = useState<ConflictMode>('skip')
  const [restoring, setRestoring] = useState(false)
  const [report, setReport] = useState<RestoreReport | null>(null)
  const validationAbortRef = useRef<AbortController | null>(null)
  const restoreLockedRef = useRef(false)
  const restoreBackup = useRestoreBackup()

  const requestClose = () => {
    if (restoreLockedRef.current || report) return
    validationAbortRef.current?.abort()
    onClose()
  }

  useEscape(requestClose, true)

  useEffect(() => {
    const controller = new AbortController()
    validationAbortRef.current = controller
    setLoading(true)
    setValidation(null)
    setErrMsg(null)
    validateBackup(file, controller.signal)
      .then((v) => {
        if (!controller.signal.aborted) setValidation(v)
      })
      .catch((e) => {
        if (!controller.signal.aborted) setErrMsg(apiErrorText(e, t('common.unknown_error')))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
        if (validationAbortRef.current === controller) validationAbortRef.current = null
      })
    return () => {
      controller.abort()
      if (validationAbortRef.current === controller) validationAbortRef.current = null
    }
  }, [file, t])

  const handleRestore = async () => {
    if (restoreLockedRef.current) return
    restoreLockedRef.current = true
    setRestoring(true)
    setErrMsg(null)
    try {
      const r = await restoreBackup.mutateAsync({ file, mode })
      setReport(r)
    } catch (e: unknown) {
      restoreLockedRef.current = false
      setErrMsg(apiErrorText(e, t('common.unknown_error')))
    } finally {
      setRestoring(false)
    }
  }

  const m = validation?.manifest
  const hasErrors = (validation?.errors?.length ?? 0) > 0

  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, true)

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={t('backup.dialog_title')}
    >
      <div className="fx-modal" style={{ maxWidth: 640 }}>
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">{t('backup.dialog_kicker_short')}</div>
            <h2 className="fx-modal-title">{t('backup.dialog_title_short')}</h2>
            <div style={{ fontSize: 12, color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)' }}>
              {file.name}
            </div>
          </div>
          <button className="fx-confirm-x" onClick={requestClose} disabled={restoring || !!report} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
          <div className="fx-modal-col">
            {loading && <div style={{ color: 'var(--fx-ink-4)' }}>{t('common.validating')}</div>}

            {restoring && (
              <div role="status" className="fx-confirm-msg" style={{ color: 'var(--fx-ink-3)' }}>
                <Icon d={I.alert} size={14} /> {t('backup.operation_locked')}
              </div>
            )}

            {!loading && errMsg && (
              <div className="fx-confirm-msg" style={{ color: 'var(--fx-danger)' }}>
                <Icon d={I.alert} size={14} /> {errMsg}
              </div>
            )}

            {!loading && validation && (
              <>
                {m ? (
                  <ValidationSummary v={validation} t={t} />
                ) : (
                  <div style={{ color: 'var(--fx-danger)' }}>
                    {t('backup.manifest_invalid')}
                  </div>
                )}

                {validation.errors.length > 0 && (
                  <div style={{ background: 'rgba(244,63,94,0.08)', borderRadius: 8, padding: 10, fontSize: 12, color: 'var(--fx-danger)' }}>
                    {validation.errors.map((e, i) => <div key={i}>✗ {e}</div>)}
                  </div>
                )}

                {!hasErrors && !report && m && (
                  <>
                    <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)', marginTop: 8 }}>
                      {t('backup.mode_section_short')}
                    </div>
                    <ConflictModePicker
                      value={mode}
                      onChange={setMode}
                      disabled={restoring}
                      wipeDanger
                      labels={{
                        skipTitle: t('backup.mode_skip_title'),
                        skipDesc: t('backup.mode_skip_desc', { links: validation.conflicts.links, tags: validation.conflicts.tags }),
                        duplicateTitle: t('backup.mode_duplicate_title'),
                        duplicateDesc: t('backup.mode_duplicate_desc'),
                        wipeTitle: t('backup.mode_wipe_title'),
                        wipeDesc: t('backup.mode_wipe_desc'),
                      }}
                    />
                  </>
                )}

                {report && <RestoreReportBlock r={report} t={t} />}
              </>
            )}
          </div>
        </div>

        <footer className="fx-modal-foot">
          {report ? (
            <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={onRestored}>
              {t('common.done')}
              <Icon d={I.check} size={14} stroke={2} />
            </button>
          ) : (
            <>
              <button className="fx-confirm-btn" onClick={requestClose} disabled={restoring}>
                {t('common.cancel')}
              </button>
              <button
                className={'fx-confirm-btn ' + (mode === 'wipe' ? 'fx-confirm-btn-danger' : 'fx-confirm-btn-primary')}
                onClick={handleRestore}
                disabled={!validation || hasErrors || restoring}
              >
                {restoring
                  ? t('backup.submit_restoring')
                  : mode === 'wipe'
                    ? t('backup.submit_restore_wipe')
                    : t('backup.submit_restore')}
                <Icon d={I.arrowR} size={14} stroke={2} />
              </button>
            </>
          )}
        </footer>
      </div>
    </div>
  )
}

function ValidationSummary({ v, t }: { v: BackupValidation; t: TFunction }) {
  const m = v.manifest
  if (!m) return null
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <Row label={t('backup.summary_manifest')} value={t('backup.summary_manifest_value', { status: v.ok ? '✓' : '✗', version: m.version, schema: m.schema_version })} />
      <Row label={t('backup.summary_content')} value={t('backup.summary_content_value', { links: m.counts.links, notes: m.counts.notes, tags: m.counts.tags, folders: m.counts.folders })} />
      <Row label={t('backup.summary_clicks')} value={t('backup.summary_clicks_value', { count: m.counts.click_logs })} />
      <Row label={t('backup.summary_files')} value={t('backup.summary_files_value', { count: m.counts.files, size: formatBytes(m.counts.file_bytes) })} />
      <Row label={t('backup.summary_conflicts')} value={t('backup.summary_conflicts_value', { links: v.conflicts.links, tags: v.conflicts.tags })} />
      {v.warnings.length > 0 && (
        <div style={{ background: 'rgba(245,158,11,0.08)', borderRadius: 8, padding: 10, fontSize: 12, color: 'var(--fx-ink-3)' }}>
          {v.warnings.map((w, i) => <div key={i}>⚠ {w}</div>)}
        </div>
      )}
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 13 }}>
      <span style={{ color: 'var(--fx-ink-4)' }}>{label}</span>
      <span style={{ color: 'var(--fx-ink)', fontFamily: 'var(--fx-mono)' }}>{value}</span>
    </div>
  )
}

function RestoreReportBlock({ r, t }: { r: RestoreReport; t: TFunction }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <Row label={t('backup.result_mode')} value={r.mode} />
      <Row label={t('backup.result_inserted')} value={t('backup.result_inserted_format', { links: r.inserted.links, notes: r.inserted.notes, tags: r.inserted.tags, folders: r.inserted.folders, clicks: r.inserted.click_logs })} />
      <Row label={t('backup.result_skipped')} value={t('backup.result_skipped_format', { links: r.skipped.links, notes: r.skipped.notes, tags: r.skipped.tags })} />
      <Row label={t('backup.result_files')} value={t('backup.result_files_format', { uploaded: r.files.uploaded, skipped: r.files.skipped })} />
      <Row label={t('backup.result_duration')} value={t('backup.report_duration_value', { value: (r.duration_ms / 1000).toFixed(2) })} />
      {r.warnings.length > 0 && (
        <div style={{ background: 'rgba(245,158,11,0.08)', borderRadius: 8, padding: 10, fontSize: 12, color: 'var(--fx-ink-3)' }}>
          {r.warnings.map((w, i) => <div key={i}>⚠ {w}</div>)}
        </div>
      )}
    </div>
  )
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

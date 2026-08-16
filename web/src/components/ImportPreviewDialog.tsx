import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import {
  validateImport,
  useApplyImport,
  type ImportFormat,
  type ImportMode,
  type ImportResult,
  type ImportValidation,
} from '../api/importer'

type Props = {
  file: File
  format: ImportFormat
  onClose: () => void
  onApplied: () => void
}

export function ImportPreviewDialog({ file, format, onClose, onApplied }: Props) {
  const { t } = useTranslation()
  const [validation, setValidation] = useState<ImportValidation | null>(null)
  const [loading, setLoading] = useState(true)
  const [errMsg, setErrMsg] = useState<string | null>(null)
  const [mode, setMode] = useState<ImportMode>('skip')
  const [excluded, setExcluded] = useState<Set<string>>(new Set())
  const [applying, setApplying] = useState(false)
  const [report, setReport] = useState<ImportResult | null>(null)
  const validationAbortRef = useRef<AbortController | null>(null)
  const applyAbortRef = useRef<AbortController | null>(null)
  const applyLockedRef = useRef(false)
  const applyImport = useApplyImport()

  const requestClose = () => {
    if (applyLockedRef.current || report) return
    validationAbortRef.current?.abort()
    onClose()
  }

  useEscape(requestClose, true)

  useEffect(() => {
    const controller = new AbortController()
    validationAbortRef.current = controller
    setLoading(true)
    setValidation(null)
    setExcluded(new Set())
    setErrMsg(null)
    validateImport(file, format, controller.signal)
      .then((v) => { if (!controller.signal.aborted) setValidation(v) })
      .catch((e) => {
        if (!controller.signal.aborted) setErrMsg(extractErr(e, t('common.unknown_error')))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
        if (validationAbortRef.current === controller) validationAbortRef.current = null
      })
    return () => {
      controller.abort()
      if (validationAbortRef.current === controller) validationAbortRef.current = null
    }
  }, [file, format, t])

  useEffect(() => () => applyAbortRef.current?.abort(), [file, format])

  // Effective counts after the user's folder exclusions.
  const effectiveCounts = useMemo(() => {
    if (!validation) return { links: 0, folders: 0, conflicts: 0 }
    let links = validation.ungrouped.links
    let conflicts = validation.ungrouped.conflicts
    let folders = 0
    for (const folder of validation.folders) {
      if (excluded.has(folder.path)) continue
      links += folder.count
      conflicts += folder.conflicts
      folders++
    }
    return { links, folders, conflicts }
  }, [validation, excluded])

  const handleApply = async () => {
    if (applyLockedRef.current) return
    applyLockedRef.current = true
    const controller = new AbortController()
    applyAbortRef.current = controller
    setApplying(true)
    setErrMsg(null)
    try {
      const r = await applyImport.mutateAsync({
        file,
        format,
        mode,
        excludeFolders: Array.from(excluded),
        signal: controller.signal,
      })
      if (!controller.signal.aborted) setReport(r)
    } catch (e: unknown) {
      if (!controller.signal.aborted) {
        applyLockedRef.current = false
        setErrMsg(extractErr(e, t('common.unknown_error')))
      }
    } finally {
      if (applyAbortRef.current === controller) applyAbortRef.current = null
      if (!controller.signal.aborted) setApplying(false)
    }
  }

  const toggle = (path: string) => {
    setExcluded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }
  const selectAll = () => setExcluded(new Set())
  const selectNone = () => setExcluded(new Set((validation?.folders ?? []).map((f) => f.path)))

  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, true)

  return (
    <div ref={dialogRef} className="fx-overlay fx-overlay-modal" role="dialog" aria-modal="true" aria-label={t('import.preview_title')}>
      <div className="fx-modal" style={{ maxWidth: 720 }}>
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">{t('import.preview_kicker')}</div>
            <h2 className="fx-modal-title">{t('import.preview_title')}</h2>
            <div style={{ fontSize: 12, color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)' }}>
              {file.name} · {format === 'netscape' ? 'Bookmarks HTML' : 'Foldex JSON'}
            </div>
          </div>
          <button className="fx-confirm-x" onClick={requestClose} disabled={applying || !!report} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
          <div className="fx-modal-col">
            {loading && <div style={{ color: 'var(--fx-ink-4)' }}>{t('common.validating')}</div>}

            {applying && (
              <div role="status" className="fx-confirm-msg" style={{ color: 'var(--fx-ink-3)' }}>
                <Icon d={I.alert} size={14} /> {t('import.operation_locked')}
              </div>
            )}

            {errMsg && (
              <div className="fx-confirm-msg" style={{ color: 'var(--fx-danger)' }}>
                <Icon d={I.alert} size={14} /> {errMsg}
              </div>
            )}

            {validation && !report && (
              <>
                <Counts validation={validation} effective={effectiveCounts} t={t} />

                <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)', marginTop: 6 }}>
                  {t('import.mode_section')}
                </div>
                <ModePicker value={mode} onChange={setMode} conflicts={validation.conflicts.links} disabled={applying} t={t} />

                {validation.folders.length > 0 && (
                  <>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginTop: 8 }}>
                      <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)' }}>
                        {t('import.folders_section_title')}
                      </div>
                      <div style={{ display: 'flex', gap: 6 }}>
                        <button type="button" className="fx-pillbtn" onClick={selectAll} disabled={applying} style={{ fontSize: 11 }}>{t('import.select_all')}</button>
                        <button type="button" className="fx-pillbtn" onClick={selectNone} disabled={applying} style={{ fontSize: 11 }}>{t('import.select_none')}</button>
                      </div>
                    </div>
                    <FolderList folders={validation.folders} excluded={excluded} onToggle={toggle} disabled={applying} />
                  </>
                )}
              </>
            )}

            {report && <ResultBlock r={report} t={t} />}
          </div>
        </div>

        <footer className="fx-modal-foot">
          {report ? (
            <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={onApplied}>
              {t('import.submit_done')}
              <Icon d={I.check} size={14} stroke={2} />
            </button>
          ) : (
            <>
              <button className="fx-confirm-btn" onClick={requestClose} disabled={applying}>{t('common.cancel')}</button>
              <button
                className={'fx-confirm-btn ' + (mode === 'wipe' ? 'fx-confirm-btn-danger' : 'fx-confirm-btn-primary')}
                onClick={handleApply}
                disabled={!validation || applying || effectiveCounts.links === 0}
              >
                {applying ? (
                  <>
                    <span className="fx-spinner" aria-hidden="true" /> {t('import.submit_importing')}
                  </>
                ) : mode === 'wipe' ? (
                  t('import.submit_wipe')
                ) : (
                  t('import.submit_apply', { count: effectiveCounts.links })
                )}
                {!applying && <Icon d={I.arrowR} size={14} stroke={2} />}
              </button>
            </>
          )}
        </footer>
      </div>
    </div>
  )
}

function Counts({
  validation, effective, t,
}: {
  validation: ImportValidation
  effective: { links: number; folders: number; conflicts: number }
  t: TFunction
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <Row label={t('import.counts_file')} value={t('import.counts_format', { links: validation.counts.links, folders: validation.counts.folders, tags: validation.counts.tags })} />
      <Row label={t('import.counts_existing')} value={t('import.counts_format_links_dup', { links: validation.conflicts.links, tags: validation.conflicts.tags })} />
      {(effective.links !== validation.counts.links || effective.folders !== validation.counts.folders) && (
        <Row
          label={t('import.counts_effective')}
          value={t('import.counts_format_effective', { links: effective.links, folders: effective.folders, duplicates: effective.conflicts })}
          accent
        />
      )}
      {validation.warnings.length > 0 && (
        <div style={{ background: 'rgba(245,158,11,0.08)', borderRadius: 8, padding: 10, fontSize: 12, color: 'var(--fx-ink-3)' }}>
          {validation.warnings.map((w, i) => <div key={i}>⚠ {w}</div>)}
        </div>
      )}
    </div>
  )
}

function Row({ label, value, accent }: { label: string; value: string; accent?: boolean }) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 13 }}>
      <span style={{ color: 'var(--fx-ink-4)' }}>{label}</span>
      <span style={{ color: accent ? 'var(--fx-accent)' : 'var(--fx-ink)', fontFamily: 'var(--fx-mono)', fontWeight: accent ? 700 : 400 }}>{value}</span>
    </div>
  )
}

function ModePicker({
  value, onChange, conflicts, disabled, t,
}: {
  value: ImportMode
  onChange: (m: ImportMode) => void
  conflicts: number
  disabled: boolean
  t: TFunction
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <ModeOption
        active={value === 'skip'}
        disabled={disabled}
        onClick={() => onChange('skip')}
        title={t('import.mode_skip_title')}
        desc={t('import.mode_skip_desc', { count: conflicts })}
      />
      <ModeOption
        active={value === 'duplicate'}
        disabled={disabled}
        onClick={() => onChange('duplicate')}
        title={t('import.mode_duplicate_title')}
        desc={t('import.mode_duplicate_desc')}
      />
      <ModeOption
        active={value === 'wipe'}
        disabled={disabled}
        onClick={() => onChange('wipe')}
        title={t('import.mode_wipe_title')}
        desc={t('import.mode_wipe_desc', { count: conflicts })}
        danger
      />
    </div>
  )
}

function ModeOption({
  active, onClick, title, desc, danger, disabled,
}: {
  active: boolean
  onClick: () => void
  title: string
  desc: string
  danger?: boolean
  disabled: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      style={{
        textAlign: 'left',
        padding: '10px 12px',
        borderRadius: 10,
        border: active ? `1.5px solid ${danger ? 'var(--fx-danger)' : 'var(--fx-accent)'}` : '1px solid var(--fx-border)',
        background: active ? (danger ? 'rgba(244,63,94,0.06)' : 'rgba(99,102,241,0.06)') : 'transparent',
        cursor: disabled ? 'not-allowed' : 'pointer',
        display: 'flex',
        flexDirection: 'column',
        gap: 3,
      }}
    >
      <span style={{ fontSize: 13, fontWeight: 700, color: danger ? 'var(--fx-danger)' : 'var(--fx-ink)' }}>{title}</span>
      <span style={{ fontSize: 11.5, color: 'var(--fx-ink-3)' }}>{desc}</span>
    </button>
  )
}

function FolderList({
  folders, excluded, onToggle, disabled,
}: {
  folders: { path: string; name: string; count: number }[]
  excluded: Set<string>
  onToggle: (path: string) => void
  disabled: boolean
}) {
  return (
    <ul style={{ listStyle: 'none', margin: 0, display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 280, overflowY: 'auto', border: '1px solid var(--fx-border)', borderRadius: 10, padding: 6 }}>
      {folders.map((f) => {
        const checked = !excluded.has(f.path)
        return (
          <li key={f.path}>
            <label style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '6px 8px', borderRadius: 6, cursor: 'pointer', opacity: checked ? 1 : 0.5 }}>
              <input
                type="checkbox"
                checked={checked}
                onChange={() => onToggle(f.path)}
                disabled={disabled}
                style={{ accentColor: 'var(--fx-accent)' }}
              />
              <span style={{ fontSize: 13, color: 'var(--fx-ink)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {f.name}
              </span>
              <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 11, color: 'var(--fx-ink-4)' }}>
                {f.count}
              </span>
            </label>
          </li>
        )
      })}
    </ul>
  )
}

function ResultBlock({ r, t }: { r: ImportResult; t: TFunction }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <Row label={t('import.result_mode')} value={r.mode} />
      <Row label={t('import.result_imported')} value={t('import.result_link', { count: r.imported })} />
      <Row label={t('import.result_skipped')} value={`${r.skipped}`} />
      {r.wiped > 0 && <Row label={t('import.result_wiped')} value={`${r.wiped}`} />}
      {r.warnings && r.warnings.length > 0 && (
        <div style={{ background: 'rgba(245,158,11,0.08)', borderRadius: 8, padding: 10, fontSize: 12, color: 'var(--fx-ink-3)', maxHeight: 200, overflowY: 'auto' }}>
          {r.warnings.map((w, i) => <div key={i}>⚠ {w}</div>)}
        </div>
      )}
    </div>
  )
}

function extractErr(e: unknown, fallback: string): string {
  const obj = e as { response?: { data?: { error?: { message?: string } } }; message?: string }
  return obj?.response?.data?.error?.message ?? obj?.message ?? fallback
}

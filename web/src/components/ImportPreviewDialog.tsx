import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useImportPreview } from '../hooks/useImportPreview'
import type { ImportFormat, ImportResult, ImportValidation } from '../api/importer'
import { ConflictModePicker } from './ConflictModePicker'

type Props = {
  file: File
  format: ImportFormat
  onClose: () => void
  onApplied: () => void
}

export function ImportPreviewDialog({ file, format, onClose, onApplied }: Props) {
  const { t } = useTranslation()
  const preview = useImportPreview(file, format)
  const {
    phase, validation, mode, setMode, excluded, toggle, selectAll, selectNone,
    effectiveCounts, errMsg, report, apply, requestClose: tryClose, canApply, canClose,
  } = preview

  const requestClose = () => {
    if (tryClose()) onClose()
  }

  useEscape(requestClose, true)

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
          <button className="fx-confirm-x" onClick={requestClose} disabled={!canClose} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
          <div className="fx-modal-col">
            {phase === 'loading' && <div style={{ color: 'var(--fx-ink-4)' }}>{t('common.validating')}</div>}

            {phase === 'applying' && (
              <div role="status" className="fx-confirm-msg" style={{ color: 'var(--fx-ink-3)' }}>
                <Icon d={I.alert} size={14} /> {t('import.operation_locked')}
              </div>
            )}

            {errMsg && (
              <div className="fx-confirm-msg" style={{ color: 'var(--fx-danger)' }}>
                <Icon d={I.alert} size={14} /> {errMsg}
              </div>
            )}

            {validation && phase !== 'done' && (
              <>
                <Counts validation={validation} effective={effectiveCounts} t={t} />

                <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)', marginTop: 6 }}>
                  {t('import.mode_section')}
                </div>
                <ConflictModePicker
                  value={mode}
                  onChange={setMode}
                  disabled={phase === 'applying'}
                  wipeDanger
                  labels={{
                    skipTitle: t('import.mode_skip_title'),
                    skipDesc: t('import.mode_skip_desc', { count: validation.conflicts.links }),
                    duplicateTitle: t('import.mode_duplicate_title'),
                    duplicateDesc: t('import.mode_duplicate_desc'),
                    wipeTitle: t('import.mode_wipe_title'),
                    wipeDesc: t('import.mode_wipe_desc', { count: validation.conflicts.links }),
                  }}
                />

                {validation.folders.length > 0 && (
                  <>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginTop: 8 }}>
                      <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)' }}>
                        {t('import.folders_section_title')}
                      </div>
                      <div style={{ display: 'flex', gap: 6 }}>
                        <button type="button" className="fx-pillbtn" onClick={selectAll} disabled={phase === 'applying'} style={{ fontSize: 11 }}>{t('import.select_all')}</button>
                        <button type="button" className="fx-pillbtn" onClick={selectNone} disabled={phase === 'applying'} style={{ fontSize: 11 }}>{t('import.select_none')}</button>
                      </div>
                    </div>
                    <FolderList folders={validation.folders} excluded={excluded} onToggle={toggle} disabled={phase === 'applying'} />
                  </>
                )}
              </>
            )}

            {phase === 'done' && report && <ResultBlock r={report} t={t} />}
          </div>
        </div>

        <footer className="fx-modal-foot">
          {phase === 'done' ? (
            <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={onApplied}>
              {t('import.submit_done')}
              <Icon d={I.check} size={14} stroke={2} />
            </button>
          ) : (
            <>
              <button className="fx-confirm-btn" onClick={requestClose} disabled={!canClose}>{t('common.cancel')}</button>
              <button
                className={'fx-confirm-btn ' + (mode === 'wipe' ? 'fx-confirm-btn-danger' : 'fx-confirm-btn-primary')}
                onClick={() => { void apply() }}
                disabled={!canApply}
              >
                {phase === 'applying' ? (
                  <>
                    <span className="fx-spinner" aria-hidden="true" /> {t('import.submit_importing')}
                  </>
                ) : mode === 'wipe' ? (
                  t('import.submit_wipe')
                ) : (
                  t('import.submit_apply', { count: effectiveCounts.links })
                )}
                {phase !== 'applying' && <Icon d={I.arrowR} size={14} stroke={2} />}
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



import { useEffect, useMemo, useRef, useState } from 'react'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import {
  applyImport,
  validateImport,
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
  const [validation, setValidation] = useState<ImportValidation | null>(null)
  const [loading, setLoading] = useState(true)
  const [errMsg, setErrMsg] = useState<string | null>(null)
  const [mode, setMode] = useState<ImportMode>('skip')
  const [excluded, setExcluded] = useState<Set<string>>(new Set())
  const [applying, setApplying] = useState(false)
  const [report, setReport] = useState<ImportResult | null>(null)

  useEscape(onClose, true)

  useEffect(() => {
    let alive = true
    setLoading(true)
    setErrMsg(null)
    validateImport(file, format)
      .then((v) => { if (alive) setValidation(v) })
      .catch((e) => { if (alive) setErrMsg(extractErr(e)) })
      .finally(() => alive && setLoading(false))
    return () => { alive = false }
  }, [file, format])

  // Effective counts after the user's folder exclusions.
  const effectiveCounts = useMemo(() => {
    if (!validation) return { links: 0, folders: 0, conflicts: 0 }
    let links = 0
    let conflicts = 0
    for (const l of validation.links) {
      if (l.folder && excluded.has(l.folder)) continue
      links++
      if (l.conflict) conflicts++
    }
    const folders = validation.folders.filter((f) => !excluded.has(f.path)).length
    return { links, folders, conflicts }
  }, [validation, excluded])

  const handleApply = async () => {
    setApplying(true)
    setErrMsg(null)
    try {
      const r = await applyImport(file, format, mode, Array.from(excluded))
      setReport(r)
    } catch (e: unknown) {
      setErrMsg(extractErr(e))
    } finally {
      setApplying(false)
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
    <div ref={dialogRef} className="fx-overlay fx-overlay-modal" role="dialog" aria-modal="true" aria-label="Revisar importação">
      <div className="fx-modal" style={{ maxWidth: 720 }}>
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">📥 IMPORTAR</div>
            <h2 className="fx-modal-title">Revisar antes de importar</h2>
            <div style={{ fontSize: 12, color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)' }}>
              {file.name} · {format === 'netscape' ? 'Bookmarks HTML' : 'Foldex JSON'}
            </div>
          </div>
          <button className="fx-confirm-x" onClick={onClose} aria-label="close">
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
          <div className="fx-modal-col">
            {loading && <div style={{ color: 'var(--fx-ink-4)' }}>Validando…</div>}

            {errMsg && (
              <div className="fx-confirm-msg" style={{ color: 'var(--fx-danger)' }}>
                <Icon d={I.alert} size={14} /> {errMsg}
              </div>
            )}

            {validation && !report && (
              <>
                <Counts validation={validation} effective={effectiveCounts} />

                <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)', marginTop: 6 }}>
                  Modo de importação
                </div>
                <ModePicker value={mode} onChange={setMode} conflicts={validation.conflicts.links} />

                {validation.folders.length > 0 && (
                  <>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginTop: 8 }}>
                      <div style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)' }}>
                        Pastas a importar
                      </div>
                      <div style={{ display: 'flex', gap: 6 }}>
                        <button type="button" className="fx-pillbtn" onClick={selectAll} style={{ fontSize: 11 }}>todas</button>
                        <button type="button" className="fx-pillbtn" onClick={selectNone} style={{ fontSize: 11 }}>nenhuma</button>
                      </div>
                    </div>
                    <FolderList folders={validation.folders} excluded={excluded} onToggle={toggle} />
                  </>
                )}
              </>
            )}

            {report && <ResultBlock r={report} />}
          </div>
        </div>

        <footer className="fx-modal-foot">
          {report ? (
            <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={onApplied}>
              Concluído
              <Icon d={I.check} size={14} stroke={2} />
            </button>
          ) : (
            <>
              <button className="fx-confirm-btn" onClick={onClose}>Cancelar</button>
              <button
                className={'fx-confirm-btn ' + (mode === 'wipe' ? 'fx-confirm-btn-danger' : 'fx-confirm-btn-primary')}
                onClick={handleApply}
                disabled={!validation || applying || effectiveCounts.links === 0}
              >
                {applying ? (
                  <>
                    <span className="fx-spinner" aria-hidden="true" /> Importando…
                  </>
                ) : mode === 'wipe' ? (
                  '⚠ Importar (substitui duplicados)'
                ) : (
                  `Importar ${effectiveCounts.links} ${effectiveCounts.links === 1 ? 'link' : 'links'}`
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
  validation, effective,
}: {
  validation: ImportValidation
  effective: { links: number; folders: number; conflicts: number }
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <Row label="Arquivo contém" value={`${validation.counts.links} links · ${validation.counts.folders} pastas · ${validation.counts.tags} tags`} />
      <Row label="Já existem no foldex" value={`${validation.conflicts.links} links · ${validation.conflicts.tags} tags`} />
      {(effective.links !== validation.counts.links || effective.folders !== validation.counts.folders) && (
        <Row
          label="Após exclusões"
          value={`${effective.links} links · ${effective.folders} pastas · ${effective.conflicts} duplicados`}
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
  value, onChange, conflicts,
}: {
  value: ImportMode
  onChange: (m: ImportMode) => void
  conflicts: number
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <ModeOption
        active={value === 'skip'}
        onClick={() => onChange('skip')}
        title="Pular duplicados (recomendado)"
        desc={`Mantém o que já existe; importa só os novos. ${conflicts} links serão pulados.`}
      />
      <ModeOption
        active={value === 'duplicate'}
        onClick={() => onChange('duplicate')}
        title="Duplicar"
        desc="Tenta importar tudo. Links com URL idêntica caem pra skip + warning (URL é UNIQUE)."
      />
      <ModeOption
        active={value === 'wipe'}
        onClick={() => onChange('wipe')}
        title="⚠ Apagar duplicados e re-importar"
        desc={`DESTRUTIVO por link. Apaga ${conflicts} links existentes (e seus cliques/tags) e re-importa os do arquivo. Links que NÃO estão no arquivo permanecem intactos.`}
        danger
      />
    </div>
  )
}

function ModeOption({
  active, onClick, title, desc, danger,
}: {
  active: boolean
  onClick: () => void
  title: string
  desc: string
  danger?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        textAlign: 'left',
        padding: '10px 12px',
        borderRadius: 10,
        border: active ? `1.5px solid ${danger ? 'var(--fx-danger)' : 'var(--fx-accent)'}` : '1px solid var(--fx-border)',
        background: active ? (danger ? 'rgba(244,63,94,0.06)' : 'rgba(99,102,241,0.06)') : 'transparent',
        cursor: 'pointer',
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
  folders, excluded, onToggle,
}: {
  folders: { path: string; name: string; count: number }[]
  excluded: Set<string>
  onToggle: (path: string) => void
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

function ResultBlock({ r }: { r: ImportResult }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <Row label="Modo" value={r.mode} />
      <Row label="Importados" value={`${r.imported} ${r.imported === 1 ? 'link' : 'links'}`} />
      <Row label="Pulados" value={`${r.skipped}`} />
      {r.wiped > 0 && <Row label="Apagados" value={`${r.wiped}`} />}
      {r.warnings && r.warnings.length > 0 && (
        <div style={{ background: 'rgba(245,158,11,0.08)', borderRadius: 8, padding: 10, fontSize: 12, color: 'var(--fx-ink-3)', maxHeight: 200, overflowY: 'auto' }}>
          {r.warnings.map((w, i) => <div key={i}>⚠ {w}</div>)}
        </div>
      )}
    </div>
  )
}

function extractErr(e: unknown): string {
  const obj = e as { response?: { data?: { error?: { message?: string } } }; message?: string }
  return obj?.response?.data?.error?.message ?? obj?.message ?? 'erro desconhecido'
}

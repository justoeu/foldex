import { useEffect, useRef, useState } from 'react'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import {
  restoreBackup,
  validateBackup,
  type BackupValidation,
  type RestoreReport,
} from '../api/backup'

type Mode = 'wipe' | 'skip' | 'duplicate'

type Props = {
  file: File
  onClose: () => void
  onRestored: () => void
}

export function BackupRestoreDialog({ file, onClose, onRestored }: Props) {
  const [validation, setValidation] = useState<BackupValidation | null>(null)
  const [loading, setLoading] = useState(true)
  const [errMsg, setErrMsg] = useState<string | null>(null)
  const [mode, setMode] = useState<Mode>('skip')
  const [restoring, setRestoring] = useState(false)
  const [report, setReport] = useState<RestoreReport | null>(null)

  useEscape(onClose, true)

  useEffect(() => {
    let alive = true
    setLoading(true)
    setErrMsg(null)
    validateBackup(file)
      .then((v) => {
        if (alive) setValidation(v)
      })
      .catch((e) => {
        if (alive) setErrMsg(extractErr(e))
      })
      .finally(() => alive && setLoading(false))
    return () => { alive = false }
  }, [file])

  const handleRestore = async () => {
    setRestoring(true)
    setErrMsg(null)
    try {
      const r = await restoreBackup(file, mode)
      setReport(r)
    } catch (e: unknown) {
      setErrMsg(extractErr(e))
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
      aria-label="Restaurar backup"
    >
      <div className="fx-modal" style={{ maxWidth: 640 }}>
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">💾 RESTAURAR</div>
            <h2 className="fx-modal-title">Revisar backup</h2>
            <div style={{ fontSize: 12, color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)' }}>
              {file.name}
            </div>
          </div>
          <button className="fx-confirm-x" onClick={onClose} aria-label="close">
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
          <div className="fx-modal-col">
            {loading && <div style={{ color: 'var(--fx-ink-4)' }}>Validando…</div>}

            {!loading && errMsg && (
              <div className="fx-confirm-msg" style={{ color: 'var(--fx-danger)' }}>
                <Icon d={I.alert} size={14} /> {errMsg}
              </div>
            )}

            {!loading && validation && (
              <>
                {m ? (
                  <ValidationSummary v={validation} />
                ) : (
                  <div style={{ color: 'var(--fx-danger)' }}>
                    Manifest inválido ou ausente.
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
                      Modo de restauração
                    </div>
                    <ModePicker value={mode} onChange={setMode} conflicts={validation.conflicts} />
                  </>
                )}

                {report && <RestoreReportBlock r={report} />}
              </>
            )}
          </div>
        </div>

        <footer className="fx-modal-foot">
          {report ? (
            <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={onRestored}>
              Concluído
              <Icon d={I.check} size={14} stroke={2} />
            </button>
          ) : (
            <>
              <button className="fx-confirm-btn" onClick={onClose}>
                Cancelar
              </button>
              <button
                className={'fx-confirm-btn ' + (mode === 'wipe' ? 'fx-confirm-btn-danger' : 'fx-confirm-btn-primary')}
                onClick={handleRestore}
                disabled={!validation || hasErrors || restoring}
              >
                {restoring
                  ? 'Restaurando…'
                  : mode === 'wipe'
                    ? '⚠ Restaurar (zerar tudo)'
                    : 'Restaurar'}
                <Icon d={I.arrowR} size={14} stroke={2} />
              </button>
            </>
          )}
        </footer>
      </div>
    </div>
  )
}

function ValidationSummary({ v }: { v: BackupValidation }) {
  const m = v.manifest
  if (!m) return null
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <Row label="Manifest" value={`${v.ok ? '✓' : '✗'} ${m.version} · schema ${m.schema_version}`} />
      <Row label="Conteúdo" value={`${m.counts.links} links · ${m.counts.tags} tags · ${m.counts.folders} pastas`} />
      <Row label="Cliques" value={`${m.counts.click_logs.toLocaleString('pt-BR')} cliques`} />
      <Row label="Arquivos" value={`${m.counts.files} · ${formatBytes(m.counts.file_bytes)}`} />
      <Row label="Conflitos" value={`${v.conflicts.links} links · ${v.conflicts.tags} tags`} />
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

function ModePicker({
  value, onChange, conflicts,
}: {
  value: Mode
  onChange: (m: Mode) => void
  conflicts: { links: number; tags: number; folders: number }
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <ModeOption
        active={value === 'skip'}
        onClick={() => onChange('skip')}
        title="Pular conflitos (recomendado)"
        desc={`Mantém o atual; adiciona só o que é novo. ${conflicts.links} links e ${conflicts.tags} tags vão ser pulados.`}
      />
      <ModeOption
        active={value === 'duplicate'}
        onClick={() => onChange('duplicate')}
        title="Duplicar"
        desc="Renomeia tags conflitantes pra `nome (2)`; folders sempre são criados novos. Links com URL idêntica caem pra skip (URL é UNIQUE)."
      />
      <ModeOption
        active={value === 'wipe'}
        onClick={() => onChange('wipe')}
        title="⚠ Limpar tudo e importar"
        desc="DESTRUTIVO. Apaga TODOS os links, tags, pastas, cliques e arquivos atuais; restaura com IDs originais preservados."
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
        border: active
          ? `1.5px solid ${danger ? 'var(--fx-danger)' : 'var(--fx-accent)'}`
          : '1px solid var(--fx-border)',
        background: active
          ? danger ? 'rgba(244,63,94,0.06)' : 'rgba(99,102,241,0.06)'
          : 'transparent',
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

function RestoreReportBlock({ r }: { r: RestoreReport }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <Row label="Modo" value={r.mode} />
      <Row label="Inseridos" value={`${r.inserted.links} links · ${r.inserted.tags} tags · ${r.inserted.folders} pastas · ${r.inserted.click_logs} cliques`} />
      <Row label="Pulados" value={`${r.skipped.links} links · ${r.skipped.tags} tags`} />
      <Row label="Arquivos" value={`${r.files.uploaded} uploads · ${r.files.skipped} já existentes`} />
      <Row label="Duração" value={`${(r.duration_ms / 1000).toFixed(2)}s`} />
      {r.warnings.length > 0 && (
        <div style={{ background: 'rgba(245,158,11,0.08)', borderRadius: 8, padding: 10, fontSize: 12, color: 'var(--fx-ink-3)' }}>
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

import type { CSSProperties } from 'react'

export type ConflictMode = 'wipe' | 'skip' | 'duplicate'

function modeOptionChrome(active: boolean, danger: boolean): Pick<CSSProperties, 'border' | 'background'> {
  if (!active) {
    return { border: '1px solid var(--fx-border)', background: 'transparent' }
  }
  if (danger) {
    return { border: '1.5px solid var(--fx-danger)', background: 'rgba(244,63,94,0.06)' }
  }
  return { border: '1.5px solid var(--fx-accent)', background: 'rgba(99,102,241,0.06)' }
}

type Labels = {
  skipTitle: string
  skipDesc: string
  duplicateTitle: string
  duplicateDesc: string
  wipeTitle: string
  wipeDesc: string
}

type Props = Readonly<{
  value: ConflictMode
  onChange: (mode: ConflictMode) => void
  disabled?: boolean
  labels: Labels
  /** Wipe is always destructive; set false only if a caller must hide that encoding. */
  wipeDanger?: boolean
}>

export function ConflictModePicker({
  value,
  onChange,
  disabled = false,
  labels,
  wipeDanger = true,
}: Props) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <ModeOption
        active={value === 'skip'}
        disabled={disabled}
        onClick={() => onChange('skip')}
        title={labels.skipTitle}
        desc={labels.skipDesc}
      />
      <ModeOption
        active={value === 'duplicate'}
        disabled={disabled}
        onClick={() => onChange('duplicate')}
        title={labels.duplicateTitle}
        desc={labels.duplicateDesc}
      />
      <ModeOption
        active={value === 'wipe'}
        disabled={disabled}
        onClick={() => onChange('wipe')}
        title={labels.wipeTitle}
        desc={labels.wipeDesc}
        danger={wipeDanger}
      />
    </div>
  )
}

function ModeOption({
  active, onClick, title, desc, danger, disabled,
}: Readonly<{
  active: boolean
  onClick: () => void
  title: string
  desc: string
  danger?: boolean
  disabled: boolean
}>) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      style={{
        textAlign: 'left',
        padding: '10px 12px',
        borderRadius: 10,
        ...modeOptionChrome(active, !!danger),
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

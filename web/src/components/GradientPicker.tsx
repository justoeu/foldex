import { useRef, useState } from 'react'
import { Icon, I } from './icons'
import { makeGradient, hexToHsl, hslToHex } from '../lib/tagColor'

const DEFAULT_COLORS = [
  '#6366F1',
  '#0EA5E9',
  '#8B5CF6',
  '#EC4899',
  '#F59E0B',
  '#10B981',
  '#64748B',
  '#FFD400',
]

type Props = {
  from: string
  to: string
  onChange: (from: string, to: string) => void
}

const PAYLOAD = 'application/x-foldex-gradient-stop'

// Shared color picker for the gradient mode of TagDialog and FolderDialog.
// Two stops (Início / Fim) + a swap affordance: clicking the ↔ button flips
// the colors, AND dragging one stop onto the other does the same thing
// (iPhone-style — same gesture as merging two links into a folder).
export function GradientPicker({ from, to, onChange }: Props) {
  const swap = () => onChange(to, from)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <HueSpectrumBar from={from} to={to} onChange={onChange} />
      <div
        aria-hidden="true"
        data-tooltip="Preview do gradiente final"
        style={{
          height: 12,
          borderRadius: 6,
          background: makeGradient(from, to),
          border: '1px solid var(--fx-border)',
        }}
      />
      <div className="fx-gradient-stops">
        <Stop
          side="from"
          label="Início"
          value={from}
          onChange={(c) => onChange(c, to)}
          onSwap={swap}
        />
        <button
          type="button"
          className="fx-gradient-swap"
          onClick={swap}
          aria-label="inverter início e fim do gradiente"
          data-tooltip="Inverter Início ↔ Fim"
          data-tooltip-side="top"
        >
          <Icon d={I.swap} size={14} stroke={2} />
        </button>
        <Stop
          side="to"
          label="Fim"
          value={to}
          onChange={(c) => onChange(from, c)}
          onSwap={swap}
        />
      </div>
    </div>
  )
}

function Stop({
  side,
  label,
  value,
  onChange,
  onSwap,
}: {
  side: 'from' | 'to'
  label: string
  value: string
  onChange: (c: string) => void
  onSwap: () => void
}) {
  const [dragOver, setDragOver] = useState(false)
  const [dragging, setDragging] = useState(false)
  return (
    <div
      className={
        'fx-gradient-stop' +
        (dragOver ? ' fx-gradient-stop-over' : '') +
        (dragging ? ' fx-gradient-stop-dragging' : '')
      }
      onDragOver={(e) => {
        if (!e.dataTransfer.types.includes(PAYLOAD)) return
        e.preventDefault()
        e.dataTransfer.dropEffect = 'move'
      }}
      onDragEnter={(e) => {
        if (e.dataTransfer.types.includes(PAYLOAD)) setDragOver(true)
      }}
      onDragLeave={(e) => {
        const next = e.relatedTarget as Node | null
        if (!next || !(e.currentTarget as Node).contains(next)) setDragOver(false)
      }}
      onDrop={(e) => {
        setDragOver(false)
        const otherSide = e.dataTransfer.getData(PAYLOAD)
        if (otherSide && otherSide !== side) {
          e.preventDefault()
          onSwap()
        }
      }}
    >
      <div
        className="fx-gradient-stop-head"
        draggable
        onDragStart={(e) => {
          e.dataTransfer.setData(PAYLOAD, side)
          e.dataTransfer.effectAllowed = 'move'
          setDragging(true)
        }}
        onDragEnd={() => setDragging(false)}
        data-tooltip="Arraste para inverter"
      >
        <span className="fx-gradient-stop-grip" aria-hidden="true">
          <Icon d={I.grip} size={10} />
        </span>
        <span className="fx-gradient-stop-label">{label}</span>
        <span
          className="fx-gradient-stop-swatch"
          aria-hidden="true"
          style={{ background: value }}
        />
      </div>
      <div className="fx-gradient-stop-palette">
        {DEFAULT_COLORS.map((c) => (
          <button
            key={c}
            type="button"
            draggable={false}
            onClick={() => onChange(c)}
            aria-label={`${label} ${c}`}
            style={{
              width: 20,
              height: 20,
              borderRadius: 6,
              background: c,
              border: c === value ? '2px solid var(--fx-ink)' : '1px solid var(--fx-border)',
              cursor: 'pointer',
              padding: 0,
            }}
          />
        ))}
        <input
          type="color"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          draggable={false}
          style={{
            width: 28,
            height: 22,
            border: 0,
            background: 'transparent',
            cursor: 'pointer',
            padding: 0,
          }}
          aria-label={`${label} custom`}
        />
      </div>
    </div>
  )
}

// Full-rainbow hue spectrum bar with 2 draggable thumbs (one per stop). Drag
// a thumb → that stop's color rotates hue while keeping its saturation +
// lightness, so a dark-saturated stop stays dark/saturated when you spin it
// across the rainbow. Click anywhere on the bar (away from a thumb) snaps
// the NEAREST thumb to that position — fast way to set both colors without
// dragging.
function HueSpectrumBar({
  from,
  to,
  onChange,
}: {
  from: string
  to: string
  onChange: (f: string, t: string) => void
}) {
  const barRef = useRef<HTMLDivElement>(null)
  const [dragging, setDragging] = useState<'from' | 'to' | null>(null)
  const fromHsl = hexToHsl(from)
  const toHsl = hexToHsl(to)

  const positionFromEvent = (e: React.PointerEvent) => {
    const rect = barRef.current?.getBoundingClientRect()
    if (!rect) return 0
    return Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width))
  }

  const setStopHue = (side: 'from' | 'to', position: number) => {
    const hue = position * 360
    const base = side === 'from' ? fromHsl : toHsl
    // Sensible floor so the color stays visible even if the user started
    // from a near-neutral hex (gray/white/black).
    const s = base.s < 15 ? 70 : base.s
    const l = base.l < 15 || base.l > 85 ? 55 : base.l
    const newHex = hslToHex(hue, s, l)
    if (side === 'from') onChange(newHex, to)
    else onChange(from, newHex)
  }

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>, side?: 'from' | 'to') => {
    if (!barRef.current) return
    barRef.current.setPointerCapture(e.pointerId)
    const x = positionFromEvent(e)
    let target: 'from' | 'to'
    if (side) {
      target = side
    } else {
      const dFrom = Math.abs(x - fromHsl.h / 360)
      const dTo = Math.abs(x - toHsl.h / 360)
      target = dFrom <= dTo ? 'from' : 'to'
    }
    setDragging(target)
    setStopHue(target, x)
  }

  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!dragging) return
    setStopHue(dragging, positionFromEvent(e))
  }

  const onPointerUp = () => setDragging(null)

  return (
    <div
      ref={barRef}
      className={'fx-hue-bar' + (dragging ? ' fx-hue-bar-dragging' : '')}
      role="slider"
      aria-label="seletor de matiz para gradiente"
      onPointerDown={(e) => onPointerDown(e)}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
    >
      <HueThumb
        position={fromHsl.h / 360}
        color={from}
        label="Início"
        active={dragging === 'from'}
        onPointerDown={(e) => {
          e.stopPropagation()
          onPointerDown(e, 'from')
        }}
      />
      <HueThumb
        position={toHsl.h / 360}
        color={to}
        label="Fim"
        active={dragging === 'to'}
        onPointerDown={(e) => {
          e.stopPropagation()
          onPointerDown(e, 'to')
        }}
      />
    </div>
  )
}

function HueThumb({
  position,
  color,
  label,
  active,
  onPointerDown,
}: {
  position: number
  color: string
  label: string
  active: boolean
  onPointerDown: (e: React.PointerEvent<HTMLDivElement>) => void
}) {
  return (
    <div
      className={'fx-hue-thumb' + (active ? ' fx-hue-thumb-active' : '')}
      style={{ left: `${position * 100}%`, background: color }}
      onPointerDown={onPointerDown}
      role="presentation"
      aria-label={`${label}: ${color}`}
      data-tooltip={`${label}: ${color}`}
      data-tooltip-side="top"
    />
  )
}

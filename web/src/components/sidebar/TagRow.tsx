type Props = Readonly<{
  name: string
  color: string
  count: number
  active: boolean
  onClick: () => void
}>

export function TagRow({ name, color, count, active, onClick }: Props) {
  return (
    <button
      className={'fx-side-row' + (active ? ' fx-side-row-active' : '')}
      onClick={onClick}
    >
      <span className="fx-side-dot" style={{ background: color }} />
      <span className="fx-side-label">{name}</span>
      <span className="fx-side-count">{count}</span>
    </button>
  )
}

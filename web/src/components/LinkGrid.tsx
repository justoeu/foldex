import { LinkCard } from './LinkCard'
import type { Link } from '../api/types'

type Props = {
  links: Link[]
  isLoading: boolean
  onEdit: (l: Link) => void
}

export function LinkGrid({ links, isLoading, onEdit }: Props) {
  if (isLoading) {
    return <div style={{ padding: 48, color: 'var(--fx-ink-4)' }}>carregando…</div>
  }
  if (links.length === 0) {
    return (
      <div style={{ padding: '48px 6px', color: 'var(--fx-ink-4)' }}>
        Nada por aqui ainda. Aperte <kbd className="fx-kbd">⌘N</kbd> pra adicionar o primeiro link.
      </div>
    )
  }
  return (
    <div className="fx-grid">
      {links.map((l) => (
        <LinkCard key={l.id} link={l} onEdit={onEdit} />
      ))}
    </div>
  )
}

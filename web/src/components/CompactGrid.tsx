import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { goHref } from '../api/links'
import type { Link } from '../api/types'

type Props = {
  links: Link[]
  onEdit: (l: Link) => void
}

export function CompactGrid({ links, onEdit }: Props) {
  return (
    <div className="fx-compactgrid">
      {links.map((l) => (
        <article key={l.id} className="fx-compact">
          <Favicon link={l} size={32} />
          <div className="fx-compact-text">
            <button
              onClick={() => onEdit(l)}
              style={{
                background: 'transparent',
                border: 0,
                padding: 0,
                cursor: 'pointer',
                textAlign: 'left',
                width: '100%',
              }}
              data-tooltip="Editar"
              aria-label="Editar"
            >
              <div className="fx-compact-title">{l.title}</div>
              <div className="fx-compact-url">{l.url}</div>
            </button>
            {l.tags.length > 0 && (
              <div className="fx-compact-tags">
                {l.tags.slice(0, 2).map((t) => (
                  <TagChip key={t.id} tag={t} />
                ))}
              </div>
            )}
          </div>
          <div className="fx-compact-side">
            <div className="fx-compact-clicks">
              <Icon d={I.flame} size={11} /> {l.click_count}
            </div>
            <a
              className="fx-openbtn fx-openbtn-list"
              href={goHref(l.id)}
              target="_blank"
              rel="noopener noreferrer"
              data-tooltip="Acessar"
              aria-label={`open ${l.title}`}
            >
              Acessar
            </a>
          </div>
        </article>
      ))}
    </div>
  )
}

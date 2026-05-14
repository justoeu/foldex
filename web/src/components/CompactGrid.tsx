import { useTranslation } from 'react-i18next'
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
  const { t } = useTranslation()
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
              data-tooltip={t('common.edit')}
              aria-label={t('common.edit')}
            >
              <div className="fx-compact-title">{l.title}</div>
              <div className="fx-compact-url">{l.url}</div>
            </button>
            {l.tags.length > 0 && (
              <div className="fx-compact-tags">
                {l.tags.slice(0, 2).map((tag) => (
                  <TagChip key={tag.id} tag={tag} />
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
              data-tooltip={t('link_card.open_action')}
              aria-label={`open ${l.title}`}
            >
              {t('link_card.open_action')}
            </a>
          </div>
        </article>
      ))}
    </div>
  )
}

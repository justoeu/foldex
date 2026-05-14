import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { useConfirm } from './ConfirmDialog'
import { goHref, useDeleteLink } from '../api/links'
import type { Link } from '../api/types'

type Props = {
  links: Link[]
  onEdit: (l: Link) => void
}

export function ListView({ links, onEdit }: Props) {
  const del = useDeleteLink()
  const confirm = useConfirm()
  const askDelete = async (l: Link) => {
    const ok = await confirm({
      title: `Apagar "${l.title}"?`,
      message: <>O link e seu histórico de cliques serão removidos permanentemente.</>,
      confirmLabel: 'Apagar link',
      destructive: true,
    })
    if (ok) del.mutate(l.id)
  }
  return (
    <div className="fx-list">
      <div className="fx-list-head">
        <div>Link</div>
        <div>Tags</div>
        <div className="fx-list-num">Cliques</div>
        <div>Último</div>
        <div />
      </div>
      {links.map((l) => (
        <div key={l.id} className="fx-list-row">
          <div className="fx-list-main">
            <Favicon link={l} size={28} />
            <div className="fx-list-text">
              <div className="fx-list-title">{l.title}</div>
              <div className="fx-list-url">{l.url}</div>
            </div>
          </div>
          <div className="fx-list-tags">
            {l.tags.slice(0, 3).map((t) => (
              <TagChip key={t.id} tag={t} />
            ))}
          </div>
          <div className="fx-list-clicks">
            <span>
              <Icon d={I.flame} size={12} /> {l.click_count}
            </span>
          </div>
          <div className="fx-list-last">{shortLast(l)}</div>
          <div className="fx-list-actions">
            <button
              className="fx-iconbtn"
              data-tooltip="Editar link"
              data-tooltip-side="top"
              aria-label="edit"
              onClick={() => onEdit(l)}
            >
              <Icon d={I.pen} size={13} />
            </button>
            <button
              className="fx-iconbtn fx-iconbtn-danger"
              data-tooltip="Apagar link"
              data-tooltip-side="top"
              aria-label="delete"
              onClick={() => askDelete(l)}
            >
              <Icon d={I.trash} size={13} />
            </button>
            <a
              className="fx-openbtn fx-openbtn-list"
              href={goHref(l.id)}
              target="_blank"
              rel="noopener noreferrer"
              data-tooltip={`Acessar via /go/${l.id}`}
              data-tooltip-side="top"
              aria-label={`open ${l.title}`}
            >
              <Icon d={I.open} size={12} />
              <span>Acessar</span>
            </a>
          </div>
        </div>
      ))}
    </div>
  )
}

function shortLast(l: Link) {
  if (!l.last_clicked_at) return '—'
  const ms = Date.now() - new Date(l.last_clicked_at).getTime()
  const min = Math.round(ms / 60000)
  if (min < 60) return `${min}m`
  const h = Math.round(min / 60)
  if (h < 24) return `${h}h`
  const d = Math.round(h / 24)
  return `${d}d`
}

import { useTranslation } from 'react-i18next'
import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { goHref } from '../api/links'
import { primaryColor } from '../lib/tagColor'
import type { Folder, Link } from '../api/types'

type Sort = 'created' | 'clicks' | 'recent' | 'alpha' | 'alpha_desc'

type Props = {
  folders: Folder[]
  links: Link[]
  sort: Sort
  onEdit: (l: Link) => void
  onOpenFolder: (id: number) => void
  onEditFolder: (f: Folder) => void
}

// Compact view: dense grid of one-line rows. Honours the same --fx-cols
// CSS variable the cards grid uses, so the topbar's 3/5/8 density picker
// changes column count here too. Folders and links live side-by-side
// — folders first by default, interleaved by name in alpha sort (mirrors
// CardsView's contract from §4 invariants).
export function CompactGrid({ folders, links, sort, onEdit, onOpenFolder, onEditFolder }: Props) {
  const isAlpha = sort === 'alpha' || sort === 'alpha_desc'

  if (isAlpha) {
    type Cell =
      | { kind: 'folder'; name: string; folder: Folder }
      | { kind: 'link'; name: string; link: Link }
    const cells: Cell[] = [
      ...folders.map<Cell>((f) => ({ kind: 'folder', name: f.name, folder: f })),
      ...links.map<Cell>((l) => ({ kind: 'link', name: l.title, link: l })),
    ]
    const dir = sort === 'alpha' ? 1 : -1
    cells.sort((a, b) => dir * a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }))
    return (
      <div className="fx-compactgrid">
        {cells.map((c) =>
          c.kind === 'folder' ? (
            <CompactFolder key={`folder-${c.folder.id}`} folder={c.folder} onOpen={onOpenFolder} onEdit={onEditFolder} />
          ) : (
            <CompactLink key={`link-${c.link.id}`} link={c.link} onEdit={onEdit} />
          ),
        )}
      </div>
    )
  }

  return (
    <div className="fx-compactgrid">
      {folders.map((f) => (
        <CompactFolder key={`folder-${f.id}`} folder={f} onOpen={onOpenFolder} onEdit={onEditFolder} />
      ))}
      {links.map((l) => (
        <CompactLink key={`link-${l.id}`} link={l} onEdit={onEdit} />
      ))}
    </div>
  )
}

function CompactLink({ link: l, onEdit }: { link: Link; onEdit: (l: Link) => void }) {
  const { t } = useTranslation()
  return (
    <article className="fx-compact">
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
          href={goHref(l)}
          target="_blank"
          rel="noopener noreferrer"
          data-tooltip={t('link_card.open_action')}
          aria-label={t('common.open_link_aria', { title: l.title })}
        >
          {t('link_card.open_action')}
        </a>
      </div>
    </article>
  )
}

function CompactFolder({
  folder: f,
  onOpen,
  onEdit,
}: {
  folder: Folder
  onOpen: (id: number) => void
  onEdit: (f: Folder) => void
}) {
  const { t } = useTranslation()
  return (
    <article className="fx-compact fx-compact-folder" onDoubleClick={() => onOpen(f.id)}>
      <span
        className="fx-compact-folder-icon"
        aria-hidden="true"
        style={{ background: f.color }}
      >
        <Icon d={I.folder} size={16} stroke={2.2} />
      </span>
      <div className="fx-compact-text">
        <button
          onClick={() => onOpen(f.id)}
          style={{
            background: 'transparent',
            border: 0,
            padding: 0,
            cursor: 'pointer',
            textAlign: 'left',
            width: '100%',
          }}
          data-tooltip={t('folder_card.open_folder')}
          aria-label={t('common.open_folder_aria', { name: f.name })}
        >
          <div className="fx-compact-title" style={{ color: primaryColor(f.color) }}>{f.name}</div>
          <div className="fx-compact-url">
            {t('folder_card.links_count', { count: f.link_count })}
          </div>
        </button>
      </div>
      <div className="fx-compact-side">
        <button
          className="fx-iconbtn"
          onClick={() => onEdit(f)}
          data-tooltip={t('common.edit')}
          aria-label={t('common.edit_folder_aria', { name: f.name })}
        >
          <Icon d={I.pen} size={13} />
        </button>
      </div>
    </article>
  )
}

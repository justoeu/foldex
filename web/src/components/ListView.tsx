import { useTranslation } from 'react-i18next'
import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { useConfirm } from './ConfirmDialog'
import { goHref, useDeleteLink } from '../api/links'
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

// Table-style list view. Folders rendered as rows alongside links. Density
// picker doesn't apply here — the list is one column by design. Sorting
// rules match CardsView/CompactGrid: folders-first by default, but alpha
// modes interleave folders and links by name/title.
export function ListView({ folders, links, sort, onEdit, onOpenFolder, onEditFolder }: Props) {
  const { t } = useTranslation()
  const del = useDeleteLink()
  const confirm = useConfirm()
  const askDelete = async (l: Link) => {
    const ok = await confirm({
      title: t('link_card.delete_confirm_title', { title: l.title }),
      message: t('link_card.delete_confirm_body_short'),
      confirmLabel: t('link_card.delete_confirm_action'),
      destructive: true,
    })
    if (ok) del.mutate(l.id)
  }

  const isAlpha = sort === 'alpha' || sort === 'alpha_desc'

  type Row = { kind: 'folder'; folder: Folder } | { kind: 'link'; link: Link }
  const rows: Row[] = (() => {
    if (isAlpha) {
      type Cell = Row & { name: string }
      const cells: Cell[] = [
        ...folders.map<Cell>((f) => ({ kind: 'folder', name: f.name, folder: f })),
        ...links.map<Cell>((l) => ({ kind: 'link', name: l.title, link: l })),
      ]
      const dir = sort === 'alpha' ? 1 : -1
      cells.sort((a, b) => dir * a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }))
      return cells
    }
    return [
      ...folders.map<Row>((f) => ({ kind: 'folder', folder: f })),
      ...links.map<Row>((l) => ({ kind: 'link', link: l })),
    ]
  })()

  return (
    <div className="fx-list">
      <div className="fx-list-head">
        <div>{t('link_card.list_header_link')}</div>
        <div>{t('link_card.list_header_tags')}</div>
        <div className="fx-list-num">{t('link_card.list_header_clicks')}</div>
        <div>{t('link_card.list_header_last')}</div>
        <div />
      </div>
      {rows.map((row) =>
        row.kind === 'folder' ? (
          <FolderRow
            key={`folder-${row.folder.id}`}
            folder={row.folder}
            onOpen={onOpenFolder}
            onEdit={onEditFolder}
          />
        ) : (
          <LinkRow
            key={`link-${row.link.id}`}
            link={row.link}
            onEdit={onEdit}
            onDelete={askDelete}
          />
        ),
      )}
    </div>
  )
}

function LinkRow({
  link: l,
  onEdit,
  onDelete,
}: {
  link: Link
  onEdit: (l: Link) => void
  onDelete: (l: Link) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="fx-list-row">
      <div className="fx-list-main">
        <Favicon link={l} size={28} />
        <div className="fx-list-text">
          <div className="fx-list-title">{l.title}</div>
          <div className="fx-list-url">{l.url}</div>
        </div>
      </div>
      <div className="fx-list-tags">
        {l.tags.slice(0, 3).map((tag) => (
          <TagChip key={tag.id} tag={tag} />
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
          data-tooltip={t('link_card.edit_link')}
          data-tooltip-side="top"
          aria-label={t('common.edit')}
          onClick={() => onEdit(l)}
        >
          <Icon d={I.pen} size={13} />
        </button>
        <button
          className="fx-iconbtn fx-iconbtn-danger"
          data-tooltip={t('link_card.delete_link')}
          data-tooltip-side="top"
          aria-label={t('common.delete')}
          onClick={() => onDelete(l)}
        >
          <Icon d={I.trash} size={13} />
        </button>
        <a
          className="fx-openbtn fx-openbtn-list"
          href={goHref(l)}
          target="_blank"
          rel="noopener noreferrer"
          data-tooltip={t('link_card.open_action')}
          data-tooltip-side="top"
          aria-label={t('common.open_link_aria', { title: l.title })}
        >
          <Icon d={I.open} size={12} />
          <span>{t('link_card.open_action')}</span>
        </a>
      </div>
    </div>
  )
}

function FolderRow({
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
    <div
      className="fx-list-row fx-list-row-folder"
      onDoubleClick={() => onOpen(f.id)}
    >
      <div className="fx-list-main">
        <span
          className="fx-list-folder-icon"
          aria-hidden="true"
          style={{ background: f.color }}
        >
          <Icon d={I.folder} size={14} stroke={2.2} />
        </span>
        <div className="fx-list-text">
          <div className="fx-list-title" style={{ color: primaryColor(f.color) }}>{f.name}</div>
          <div className="fx-list-url">
            {t('folder_card.links_count', { count: f.link_count })}
          </div>
        </div>
      </div>
      {/* Tags column: empty for folders */}
      <div className="fx-list-tags" />
      {/* Clicks / last: folders don't have either — keep cells empty to
          preserve grid alignment with link rows. */}
      <div className="fx-list-clicks" />
      <div className="fx-list-last" />
      <div className="fx-list-actions">
        <button
          className="fx-iconbtn"
          data-tooltip={t('common.edit')}
          data-tooltip-side="top"
          aria-label={t('common.edit_folder_aria', { name: f.name })}
          onClick={() => onEdit(f)}
        >
          <Icon d={I.pen} size={13} />
        </button>
        <button
          className="fx-openbtn fx-openbtn-list"
          onClick={() => onOpen(f.id)}
          data-tooltip={t('folder_card.open_folder')}
          data-tooltip-side="top"
          aria-label={t('common.open_folder_aria', { name: f.name })}
        >
          <Icon d={I.open} size={12} />
          <span>{t('common.open')}</span>
        </button>
      </div>
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

import { useTranslation } from 'react-i18next'
import { Icon, I } from '../icons'
import { FolderLockButton } from '../sidebar/FolderRow'
import { Favicon } from '../Favicon'
import { TagChip } from '../TagChip'
import { goHref } from '../../api/links'
import { searchFolderTree } from '../../lib/folderTree'
import type { useCommandPaletteSearch } from '../../hooks/useCommandPaletteSearch'
import type { Link } from '../../api/types'

type Search = ReturnType<typeof useCommandPaletteSearch>

type Props = Readonly<{
  search: Search
  highlight: number
  canEdit: boolean
  onClose: () => void
  onOpenFolder?: (id: number) => void
  onRevealLink?: (link: Link) => void
  onEditLink?: (link: Link) => void
  onViewNote?: (id: number) => void
  onSelectTag?: (tag: { id: number; name: string }) => void
}>

export function PaletteGroups({ search, highlight, canEdit, onClose, onOpenFolder, onRevealLink, onEditLink, onViewNote, onSelectTag }: Props) {
  const { t } = useTranslation()
  const {
    debounced,
    suggested,
    matches,
    noteMatches,
    tagMatches,
    folderMatches,
    folderNameById,
    linksOffset,
    notesOffset,
    foldersOffset,
    tagsOffset,
    hasResults,
  } = search

  return (
    <div className="fx-cmdk-results">
      {!debounced && suggested.length > 0 && (
        <div className="fx-cmdk-group">
          <div className="fx-cmdk-grouplabel">{t('command_palette.suggested_section')}</div>
          {suggested.map((l, i) => (
            <PaletteLinkRow
              key={l.id}
              id={`fx-cmdk-row-${i}`}
              link={l}
              selected={highlight === i}
              hint={t('command_palette.clicks_count', { count: l.click_count })}
              canEdit={canEdit}
              folderName={l.folder_id != null ? folderNameById.get(l.folder_id) : undefined}
              onClose={onClose}
              onReveal={onRevealLink}
              onEdit={onEditLink}
            />
          ))}
        </div>
      )}

      {matches.length > 0 && (
        <div className="fx-cmdk-group">
          <div className="fx-cmdk-grouplabel">{t('command_palette.links_section')}</div>
          {matches.map((l, i) => (
            <PaletteLinkRow
              key={l.id}
              id={`fx-cmdk-row-${linksOffset + i}`}
              link={l}
              selected={highlight === linksOffset + i}
              hint={`/go/${l.slug}`}
              canEdit={canEdit}
              folderName={l.folder_id != null ? folderNameById.get(l.folder_id) : undefined}
              onClose={onClose}
              onReveal={onRevealLink}
              onEdit={onEditLink}
            />
          ))}
        </div>
      )}

      {noteMatches.length > 0 && (
        <div className="fx-cmdk-group">
          <div className="fx-cmdk-grouplabel">{t('command_palette.notes_section')}</div>
          {noteMatches.map((n, i) => (
            <button
              type="button"
              key={n.id}
              id={`fx-cmdk-row-${notesOffset + i}`}
              className={'fx-cmdk-row' + (highlight === notesOffset + i ? ' fx-cmdk-row-sel' : '')}
              onClick={() => {
                onViewNote?.(n.id)
                onClose()
              }}
            >
              <span className="fx-cmdk-note-icon" aria-hidden="true">
                <Icon d={I.note} size={16} />
              </span>
              <div className="fx-cmdk-main">
                <div className="fx-cmdk-title">{n.title}</div>
                {n.body_text_snippet && <div className="fx-cmdk-sub">{n.body_text_snippet}</div>}
              </div>
              <div className="fx-cmdk-tags">
                {n.tags.slice(0, 2).map((tag) => (
                  <TagChip key={tag.id} tag={tag} />
                ))}
              </div>
            </button>
          ))}
        </div>
      )}

      {folderMatches.length > 0 && (
        <div className="fx-cmdk-group">
          <div className="fx-cmdk-grouplabel">
            {t('command_palette.folders_section')}
            <span className="fx-cmdk-grouphint">
              <span className="fx-cmdk-grouphint-unit">L</span> {t('command_palette.folders_hint_links')} ·{' '}
              <span className="fx-cmdk-grouphint-unit">P</span> {t('command_palette.folders_hint_folders')}
            </span>
          </div>
          {folderMatches.map((f, i) => (
            <PaletteFolderRow
              key={f.id}
              id={`fx-cmdk-row-${foldersOffset + i}`}
              folder={f}
              selected={highlight === foldersOffset + i}
              onOpenFolder={onOpenFolder}
            />
          ))}
        </div>
      )}

      {tagMatches.length > 0 && (
        <div className="fx-cmdk-group">
          <div className="fx-cmdk-grouplabel">{t('command_palette.tags_section')}</div>
          {tagMatches.map((tag, i) => (
            <button
              type="button"
              key={tag.id}
              id={`fx-cmdk-row-${tagsOffset + i}`}
              className={'fx-cmdk-row' + (highlight === tagsOffset + i ? ' fx-cmdk-row-sel' : '')}
              onClick={() => {
                onSelectTag?.({ id: tag.id, name: tag.name })
                onClose()
              }}
            >
              <span className="fx-cmdk-tagdot" style={{ background: tag.color }} />
              <div className="fx-cmdk-main">
                <div className="fx-cmdk-title">
                  {t('command_palette.filter_by')} <b>{tag.name}</b>
                </div>
                <div className="fx-cmdk-sub">{t('command_palette.tag_links_count', { count: tag.link_count ?? 0 })}</div>
              </div>
              <span className="fx-cmdk-hint">{t('command_palette.tag_hint')}</span>
            </button>
          ))}
        </div>
      )}

      {debounced && !hasResults && (
        <div style={{ padding: 24, textAlign: 'center', color: 'var(--fx-ink-4)', fontSize: 13 }}>
          {t('command_palette.no_results')}
        </div>
      )}
    </div>
  )
}

function PaletteLinkRow({  link,
  id,
  selected,
  hint,
  canEdit,
  folderName,
  onClose,
  onReveal,
  onEdit,
}: Readonly<{
  link: Link
  id?: string
  selected?: boolean
  hint: string
  canEdit: boolean
  folderName?: string
  onClose: () => void
  onReveal?: (link: Link) => void
  onEdit?: (link: Link) => void
}>) {
  const { t } = useTranslation()
  const inFolder = link.folder_id != null
  const revealLabel = inFolder
    ? t('command_palette.reveal_in_folder', { name: folderName || t('command_palette.folder_hint') })
    : t('command_palette.reveal_on_home')

  return (
    <div id={id} className={'fx-cmdk-link-row' + (selected ? ' fx-cmdk-row-sel' : '')}>
      <a
        className="fx-cmdk-row-go"
        href={goHref(link)}
        target="_blank"
        rel="noopener noreferrer"
        onClick={onClose}
      >
        <Favicon link={link} size={22} />
        <div className="fx-cmdk-main">
          <div className="fx-cmdk-title">{link.title}</div>
          <div className="fx-cmdk-sub">{link.url}</div>
        </div>
        <div className="fx-cmdk-tags">
          {link.tags.slice(0, 2).map((tag) => (
            <TagChip key={tag.id} tag={tag} />
          ))}
        </div>
        <span className="fx-cmdk-hint">{hint}</span>
      </a>
      <div className="fx-cmdk-row-actions">
        {onReveal && (
          <button
            type="button"
            className="fx-cmdk-icon"
            aria-label={revealLabel}
            data-tooltip={revealLabel}
            data-tooltip-side="top"
            onClick={(event) => {
              event.preventDefault()
              event.stopPropagation()
              onReveal(link)
            }}
          >
            <Icon d={inFolder ? I.folder : I.home} size={14} />
          </button>
        )}
        {canEdit && onEdit && (
          <button
            type="button"
            className="fx-cmdk-icon"
            aria-label={t('command_palette.edit_link')}
            data-tooltip={t('command_palette.edit_link')}
            data-tooltip-side="top"
            onClick={(event) => {
              event.preventDefault()
              event.stopPropagation()
              onEdit(link)
            }}
          >
            <Icon d={I.pen} size={14} />
          </button>
        )}
      </div>
    </div>
  )
}

// The folder row, extracted like PaletteLinkRow: depth guides, lock badge,
// L/P counts. Own component so the palette body stays a flat list of
// groups and the row is testable in isolation as it grows.
function PaletteFolderRow({
  folder: f,
  id,
  selected,
  onOpenFolder,
}: Readonly<{
  folder: ReturnType<typeof searchFolderTree>[number]
  id?: string
  selected?: boolean
  onOpenFolder?: (id: number) => void
}>) {
  const { t } = useTranslation()
  return (
    <button
      type="button"
      id={id}
      className={
        'fx-cmdk-row fx-cmdk-folder-row' + (selected ? ' fx-cmdk-row-sel' : '')
      }
      onClick={() => onOpenFolder?.(f.id)}
      aria-label={t('command_palette.open_folder_aria', { name: f.name })}
      data-tooltip={f.path.length > 1 ? f.path.join(' / ') : f.name}
    >
      {f.depth > 0 && (
        <span className="fx-cmdk-folder-indent" aria-hidden="true">
          {Array.from({ length: f.depth }).map((_, i) => (
            <span
              key={`${f.id}-guide-${i}`}
              className={
                'fx-cmdk-folder-guide' +
                (i === f.depth - 1 ? ' fx-cmdk-folder-guide-last' : '')
              }
            />
          ))}
        </span>
      )}
      <span className="fx-cmdk-folder-icon" style={{ color: f.color }}>
        <Icon d={I.folder} size={14} />
      </span>
      <span className="fx-cmdk-folder-counts" aria-hidden="true">
        <span className="fx-cmdk-folder-count">
          {f.link_count}
          <span className="fx-cmdk-folder-unit">L</span>
        </span>
        {f.folder_count > 0 && (
          <span className="fx-cmdk-folder-count">
            {f.folder_count}
            <span className="fx-cmdk-folder-unit">P</span>
          </span>
        )}
      </span>
      <span className="fx-cmdk-folder-name">
        {f.has_password && (
          <FolderLockButton />
        )}
        {f.name}
      </span>
      <span className="fx-cmdk-hint">{t('command_palette.folder_hint')}</span>
    </button>
  )
}

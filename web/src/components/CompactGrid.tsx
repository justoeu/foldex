import { memo } from 'react'
import { useTranslation } from 'react-i18next'
import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { goHref } from '../api/links'
import { goNoteHref } from '../api/notes'
import { primaryColor } from '../lib/tagColor'
import { mergeAlphaCells } from '../lib/mergeAlphaCells'
import type { Entry, Folder, Link } from '../api/types'

type Sort = 'created' | 'clicks' | 'recent' | 'alpha' | 'alpha_desc'
type NoteEntry = Extract<Entry, { kind: 'note' }>

type Props = {
  folders: Folder[]
  entries: Entry[]
  sort: Sort
  onEdit: (l: Link) => void
  onEditNote: (id: number) => void
  onOpenFolder: (id: number) => void
  onEditFolder: (f: Folder) => void
}

// Compact view: dense grid of one-line rows. Honours the same --fx-cols
// CSS variable the cards grid uses, so the topbar's 3/5/8 density picker
// changes column count here too. Folders and entries live side-by-side —
// folders first by default, interleaved by name in alpha sort (mirrors
// CardsView's contract from §4 invariants).
export function CompactGrid({ folders, entries, sort, onEdit, onEditNote, onOpenFolder, onEditFolder }: Props) {
  const isAlpha = sort === 'alpha' || sort === 'alpha_desc'

  if (isAlpha) {
    const cells = mergeAlphaCells(folders, entries, sort === 'alpha' ? 1 : -1)
    return (
      <div className="fx-compactgrid">
        {cells.map((c) => {
          if (c.kind === 'folder') {
            return <CompactFolder key={`folder-${c.folder.id}`} folder={c.folder} onOpen={onOpenFolder} onEdit={onEditFolder} />
          }
          if (c.kind === 'link') {
            return <CompactLink key={`link-${c.entry.id}`} link={c.entry} onEdit={onEdit} />
          }
          return <CompactNote key={`note-${c.entry.id}`} note={c.entry} onEdit={onEditNote} />
        })}
      </div>
    )
  }

  return (
    <div className="fx-compactgrid">
      {folders.map((f) => (
        <CompactFolder key={`folder-${f.id}`} folder={f} onOpen={onOpenFolder} onEdit={onEditFolder} />
      ))}
      {entries.map((e) =>
        e.kind === 'link' ? (
          <CompactLink key={`link-${e.id}`} link={e} onEdit={onEdit} />
        ) : (
          <CompactNote key={`note-${e.id}`} note={e} onEdit={onEditNote} />
        ),
      )}
    </div>
  )
}

// memo on CompactLink mirrors LinkCard/LinkRow: stable props (link + onEdit
// useCallback from App) → no re-render when siblings change.
const CompactLink = memo(CompactLinkImpl)
CompactLink.displayName = 'CompactLink'

function CompactLinkImpl({ link: l, onEdit }: { link: Link; onEdit: (l: Link) => void }) {
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

// memo on CompactNote mirrors CompactLink.
const CompactNote = memo(CompactNoteImpl)
CompactNote.displayName = 'CompactNote'

function CompactNoteImpl({ note: n, onEdit }: { note: NoteEntry; onEdit: (id: number) => void }) {
  const { t } = useTranslation()
  return (
    <article className="fx-compact">
      <span className="fx-compact-note-icon" aria-hidden="true">
        <Icon d={I.note} size={18} stroke={2.2} />
      </span>
      <div className="fx-compact-text">
        <button
          onClick={() => onEdit(n.id)}
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
          <div className="fx-compact-title">{n.title}</div>
          {n.body_text_snippet && <div className="fx-compact-url">{n.body_text_snippet}</div>}
        </button>
        {n.tags.length > 0 && (
          <div className="fx-compact-tags">
            {n.tags.slice(0, 2).map((tag) => (
              <TagChip key={tag.id} tag={tag} />
            ))}
          </div>
        )}
      </div>
      <div className="fx-compact-side">
        <div className="fx-compact-clicks">
          <Icon d={I.flame} size={11} /> {n.click_count}
        </div>
        <a
          className="fx-openbtn fx-openbtn-list"
          href={goNoteHref(n)}
          target="_blank"
          rel="noopener noreferrer"
          data-tooltip={t('note_card.open_action')}
          aria-label={t('common.open_link_aria', { title: n.title })}
        >
          {t('note_card.open_action')}
        </a>
      </div>
    </article>
  )
}

// memo on CompactFolder mirrors CompactLink.
const CompactFolder = memo(CompactFolderImpl)
CompactFolder.displayName = 'CompactFolder'

function CompactFolderImpl({
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
          <div className="fx-compact-title" style={{ color: primaryColor(f.color) }}>
            {f.has_password && (
              <span
                className="fx-folder-lock-icon"
                aria-hidden="true"
                data-tooltip={t('folder_card.locked_tooltip')}
                data-tooltip-side="top"
              >
                <Icon d={I.lock} size={12} />
              </span>
            )}
            {f.name}
          </div>
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

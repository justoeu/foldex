import { memo, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { useConfirm } from './ConfirmDialog'
import { goHref, useDeleteLink } from '../api/links'
import { goNoteHref, useDeleteNote } from '../api/notes'
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

// Table-style list view. Folders rendered as rows alongside links and notes.
// Density picker doesn't apply here — the list is one column by design.
// Sorting rules mirror CardsView/CompactGrid: default order is folders-first
// then entries in the backend's already-sorted order; alpha modes interleave
// folders + entries by name/title via the shared mergeAlphaCells helper.
export function ListView({ folders, entries, sort, onEdit, onEditNote, onOpenFolder, onEditFolder }: Props) {
  const { t } = useTranslation()
  const del = useDeleteLink()
  const delNote = useDeleteNote()
  const confirm = useConfirm()
  // useCallback is REQUIRED here, not optional: this closure is passed as
  // `onDelete` to every <LinkRow>, which is React.memo'd. A new identity per
  // render would defeat the memo (every parent keystroke would re-render all
  // rows). del.mutate and confirm are stable hook returns; t from
  // useTranslation is stable per i18n instance.
  const askDelete = useCallback(
    async (l: Link) => {
      const ok = await confirm({
        title: t('link_card.delete_confirm_title', { title: l.title }),
        message: t('link_card.delete_confirm_body_short'),
        confirmLabel: t('link_card.delete_confirm_action'),
        destructive: true,
      })
      if (ok) del.mutate(l.id)
    },
    [confirm, del, t],
  )
  const askDeleteNote = useCallback(
    async (n: NoteEntry) => {
      const ok = await confirm({
        title: t('note_card.delete_confirm_title', { title: n.title }),
        message: t('note_card.delete_confirm_body'),
        confirmLabel: t('note_card.delete_confirm_action'),
        destructive: true,
      })
      if (ok) delNote.mutate(n.id)
    },
    [confirm, delNote, t],
  )

  const isAlpha = sort === 'alpha' || sort === 'alpha_desc'

  type Row =
    | { kind: 'folder'; folder: Folder }
    | { kind: 'link'; link: Link }
    | { kind: 'note'; note: NoteEntry }
  const rows: Row[] = isAlpha
    ? mergeAlphaCells(folders, entries, sort === 'alpha' ? 1 : -1).map((c) =>
        c.kind === 'folder'
          ? { kind: 'folder', folder: c.folder }
          : c.kind === 'link'
            ? { kind: 'link', link: c.entry }
            : { kind: 'note', note: c.entry },
      )
    : [
        ...folders.map<Row>((f) => ({ kind: 'folder', folder: f })),
        ...entries.map<Row>((e) => (e.kind === 'link' ? { kind: 'link', link: e } : { kind: 'note', note: e })),
      ]

  return (
    <div className="fx-list">
      <div className="fx-list-head">
        <div>{t('link_card.list_header_link')}</div>
        <div>{t('link_card.list_header_tags')}</div>
        <div className="fx-list-num">{t('link_card.list_header_clicks')}</div>
        <div>{t('link_card.list_header_last')}</div>
        <div />
      </div>
      {rows.map((row) => {
        if (row.kind === 'folder') {
          return <FolderRow key={`folder-${row.folder.id}`} folder={row.folder} onOpen={onOpenFolder} onEdit={onEditFolder} />
        }
        if (row.kind === 'link') {
          return <LinkRow key={`link-${row.link.id}`} link={row.link} onEdit={onEdit} onDelete={askDelete} />
        }
        return <NoteRow key={`note-${row.note.id}`} note={row.note} onEdit={onEditNote} onDelete={askDeleteNote} />
      })}
    </div>
  )
}

// memo guards re-render storms in long lists (100+ rows). Each row mounts
// Favicon + up to 3 TagChips and calls useTranslation; without memo, every
// parent state change (keystroke in search, sidebar toggle, optimistic click
// bump) re-renders all rows. App.tsx wires onEdit/onDelete via useCallback so
// the default shallow compare is correct — props are stable across renders.
const LinkRow = memo(LinkRowImpl)
LinkRow.displayName = 'LinkRow'

function LinkRowImpl({
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
      <div className="fx-list-last">{shortLast(l.last_clicked_at)}</div>
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

// memo mirrors LinkRow — same stable-props rationale.
const NoteRow = memo(NoteRowImpl)
NoteRow.displayName = 'NoteRow'

function NoteRowImpl({
  note: n,
  onEdit,
  onDelete,
}: {
  note: NoteEntry
  onEdit: (id: number) => void
  onDelete: (n: NoteEntry) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="fx-list-row">
      <div className="fx-list-main">
        <span className="fx-list-note-icon" aria-hidden="true">
          <Icon d={I.note} size={14} stroke={2.2} />
        </span>
        <div className="fx-list-text">
          <button
            type="button"
            className="fx-list-title fx-list-title-btn"
            onClick={() => onEdit(n.id)}
          >
            {n.title}
          </button>
          {n.body_text_snippet && <div className="fx-list-url">{n.body_text_snippet}</div>}
        </div>
      </div>
      <div className="fx-list-tags">
        {n.tags.slice(0, 3).map((tag) => (
          <TagChip key={tag.id} tag={tag} />
        ))}
      </div>
      <div className="fx-list-clicks">
        <span>
          <Icon d={I.flame} size={12} /> {n.click_count}
        </span>
      </div>
      <div className="fx-list-last">{shortLast(n.last_clicked_at)}</div>
      <div className="fx-list-actions">
        <button
          className="fx-iconbtn"
          data-tooltip={t('note_card.edit_note')}
          data-tooltip-side="top"
          aria-label={t('common.edit')}
          onClick={() => onEdit(n.id)}
        >
          <Icon d={I.pen} size={13} />
        </button>
        <button
          className="fx-iconbtn fx-iconbtn-danger"
          data-tooltip={t('note_card.delete_note')}
          data-tooltip-side="top"
          aria-label={t('common.delete')}
          onClick={() => onDelete(n)}
        >
          <Icon d={I.trash} size={13} />
        </button>
        <a
          className="fx-openbtn fx-openbtn-list"
          href={goNoteHref(n)}
          target="_blank"
          rel="noopener noreferrer"
          data-tooltip={t('note_card.open_action')}
          data-tooltip-side="top"
          aria-label={t('common.open_link_aria', { title: n.title })}
        >
          <Icon d={I.open} size={12} />
          <span>{t('note_card.open_action')}</span>
        </a>
      </div>
    </div>
  )
}

// memo on FolderRow mirrors LinkRow: stable props (folder + onOpen/onEdit
// useCallbacks from App) → no re-render when siblings change.
const FolderRow = memo(FolderRowImpl)
FolderRow.displayName = 'FolderRow'

function FolderRowImpl({
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
          <div className="fx-list-title" style={{ color: primaryColor(f.color) }}>
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

function shortLast(lastClickedAt: string | null | undefined) {
  if (!lastClickedAt) return '—'
  const ms = Date.now() - new Date(lastClickedAt).getTime()
  const min = Math.round(ms / 60000)
  if (min < 60) return `${min}m`
  const h = Math.round(min / 60)
  if (h < 24) return `${h}h`
  const d = Math.round(h / 24)
  return `${d}d`
}

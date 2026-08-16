import { memo } from 'react'
import { useTranslation } from 'react-i18next'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { goNoteHref } from '../api/notes'
import { safeImageUrl } from '../lib/url'
import type { Entry, MergeSource } from '../api/types'

export type NoteEntry = Extract<Entry, { kind: 'note' }>

type Props = {
  note: NoteEntry
  onEdit: (id: number) => void
  onMergeWith?: (source: MergeSource, targetId: number) => void
  onDelete: (note: NoteEntry) => void
  onPin: (note: NoteEntry, pinned: boolean) => void
  onOpen: (note: NoteEntry) => void
}

// Density mirrors LinkCard's densityFor: tall when a cover image is present,
// medium when there's a body snippet to show, short otherwise.
function densityFor(note: NoteEntry): 'tall' | 'medium' | 'short' {
  if (note.cover_url) return 'tall'
  if (note.body_text_snippet) return 'medium'
  return 'short'
}

export const NoteCard = memo(NoteCardImpl)
NoteCard.displayName = 'NoteCard'

function NoteCardImpl({ note, onEdit, onMergeWith, onDelete, onPin, onOpen }: Props) {
  const { t } = useTranslation()
  const previewSrc = safeImageUrl(note.cover_url)
  const density = densityFor(note)

  return (
    <article
      className={'fx-card fx-card-' + density + (note.pinned ? ' fx-card-pinned' : '')}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData('application/x-foldex-note', String(note.id))
        e.dataTransfer.effectAllowed = 'move'
      }}
      onDragOver={(e) => {
        const raw = e.dataTransfer.types
        if (!raw.includes('application/x-foldex-note') && !raw.includes('application/x-foldex-link')) return
        e.preventDefault()
        e.dataTransfer.dropEffect = 'move'
      }}
      onDrop={(e) => {
        const noteRaw = e.dataTransfer.getData('application/x-foldex-note')
        const linkRaw = e.dataTransfer.getData('application/x-foldex-link')
        const source: MergeSource | null = noteRaw
          ? { kind: 'note', id: Number(noteRaw) }
          : linkRaw
            ? { kind: 'link', id: Number(linkRaw) }
            : null
        if (!source || !source.id) return
        if (source.kind === 'note' && source.id === note.id) return
        e.preventDefault()
        onMergeWith?.(source, note.id)
      }}
    >
      <button
        className={'fx-card-pin-badge' + (note.pinned ? '' : ' fx-card-pin-off')}
        onClick={(e) => {
          e.stopPropagation()
          onPin(note, !note.pinned)
        }}
        aria-label={note.pinned ? t('note_card.unpin') : t('note_card.pin')}
        data-tooltip={note.pinned ? t('note_card.unpin_tooltip') : t('note_card.pin_top_tooltip')}
        data-tooltip-side="left"
      >
        <Icon d={I.pin} size={13} stroke={2} />
      </button>

      {/* Kind discriminator, always visible (not conditional like the pin
          badge) — opposite corner so it never competes for the pin badge's
          right-side slot. */}
      <span
        className="fx-card-note-badge"
        aria-label={t('note_card.note_badge_tooltip')}
        data-tooltip={t('note_card.note_badge_tooltip')}
        data-tooltip-side="right"
      >
        <Icon d={I.note} size={12} stroke={2} />
      </span>

      {previewSrc && (
        <a className="fx-preview fx-preview-img" href={goNoteHref(note)} target="_blank" rel="noopener noreferrer" onClick={() => onOpen(note)}>
          <img
            src={previewSrc}
            alt=""
            referrerPolicy="no-referrer"
            loading="lazy"
            decoding="async"
            style={{ width: '100%', height: '100%', objectFit: 'scale-down', display: 'block' }}
          />
        </a>
      )}
      <div className="fx-card-body">
        <header className="fx-card-head">
          <span className="fx-card-note-icon" aria-hidden="true">
            <Icon d={I.note} size={previewSrc ? 22 : 28} />
          </span>
          <div className="fx-card-head-text">
            <h3 className="fx-card-title">
              <button type="button" className="fx-card-title-link" onClick={() => onEdit(note.id)}>
                {note.title}
              </button>
            </h3>
          </div>
        </header>

        {note.body_text_snippet && <p className="fx-card-desc">{note.body_text_snippet}</p>}

        {note.tags.length > 0 && (
          <div className="fx-card-tags">
            {note.tags.map((tag) => (
              <TagChip key={tag.id} tag={tag} />
            ))}
          </div>
        )}

        <footer className="fx-card-foot">
          <div className="fx-card-meta">
            <span className="fx-meta-stat" data-tooltip={t('note_card.clicks_tooltip')} aria-label={t('note_card.clicks_tooltip')}>
              <Icon d={I.flame} size={13} /> {note.click_count}
            </span>
          </div>

          <div className="fx-card-actions">
            <button
              className="fx-iconbtn"
              data-tooltip={t('note_card.edit_note')}
              data-tooltip-side="top"
              aria-label={t('common.edit')}
              onClick={() => onEdit(note.id)}
            >
              <Icon d={I.pen} size={14} />
            </button>
            <button
              className="fx-iconbtn fx-iconbtn-danger"
              data-tooltip={t('note_card.delete_note')}
              data-tooltip-side="top"
              aria-label={t('common.delete')}
              onClick={() => onDelete(note)}
            >
              <Icon d={I.trash} size={14} />
            </button>
            <a
              className="fx-openbtn"
              href={goNoteHref(note)}
              target="_blank"
              rel="noopener noreferrer"
              data-tooltip={t('note_card.open_action')}
              data-tooltip-side="top"
              aria-label={t('common.open_link_aria', { title: note.title })}
              onClick={() => onOpen(note)}
            >
              <span className="fx-openbtn-go">{t('note_card.open_action')}</span>
              <Icon d={I.arrowR} size={14} />
            </a>
          </div>
        </footer>
      </div>
    </article>
  )
}

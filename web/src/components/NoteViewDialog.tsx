import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useNote, goNoteHref } from '../api/notes'
import { TagChip } from './TagChip'
import { relativeTime } from '../lib/time'
import type { Note } from '../api/types'

/**
 * Reads a note in place.
 *
 * "Abrir" used to be an `<a target="_blank">` to `/n/{slug}` — the PUBLIC page —
 * so reading your own note meant leaving the app, and the tab you landed on was
 * the same anonymous view a share link gives a stranger. In-app it belongs in a
 * modal, beside the grid it was opened from.
 *
 * The body is rendered with `dangerouslySetInnerHTML`, and what makes that safe
 * is not an assumption: `note.body_html` is sanitized SERVER-side on every
 * write path, including backup restore (CLAUDE.md §4). The public `/n/` route
 * renders the same string the same way.
 *
 * **This does not count a view.** `click_log` is the single source of truth for
 * views and the public `/n/` route is the only path that inserts into it, so an
 * in-app read is deliberately not one — a note you open from your own grid is
 * not a visit. The footer keeps a link to the public page for when a real visit
 * is what you want.
 */
export function NoteViewDialog({
  noteId,
  onClose,
  onEdit,
}: {
  noteId: number
  onClose: () => void
  onEdit: (note: Note) => void
}) {
  const { t } = useTranslation()
  const dialogRef = useRef<HTMLDivElement>(null)
  useEscape(onClose, true)
  useFocusTrap(dialogRef, true)

  const noteQuery = useNote(noteId)
  const note = noteQuery.data
  // Same rule as the card: a cover whose object is gone is hidden, not left as
  // a broken-image icon on top of the text.
  const [coverErrored, setCoverErrored] = useState(false)

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={note?.title ?? t('note_view.loading')}
    >
      <div className="fx-modal" style={{ maxWidth: 760 }}>
        <header className="fx-modal-head">
          <div style={{ minWidth: 0 }}>
            <div className="fx-modal-kicker fx-modal-kicker-note">
              <Icon d={I.note} size={12} /> {t('note_view.kicker')}
            </div>
            <h2 className="fx-modal-title">{note?.title ?? t('note_view.loading')}</h2>
          </div>
          <button type="button" className="fx-confirm-x" onClick={onClose} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-noteview-body">
          {noteQuery.isError ? (
            <div className="fx-noteview-empty">
              <Icon d={I.alert} size={16} />
              <span>{t('note_view.load_failed')}</span>
              <button
                type="button"
                className="fx-confirm-btn"
                onClick={() => { void noteQuery.refetch() }}
                disabled={noteQuery.isFetching}
              >
                <Icon d={I.refresh} size={13} /> {t('note_dialog.load_retry')}
              </button>
            </div>
          ) : !note ? (
            <div className="fx-noteview-empty">{t('note_view.loading')}</div>
          ) : (
            <>
              {note.cover_url && !coverErrored && (
                <img
                  className="fx-noteview-cover"
                  src={note.cover_url}
                  alt=""
                  onError={() => setCoverErrored(true)}
                />
              )}

              {/* Everything ABOUT the note, before the note itself: tags decide
                  whether this is the one you meant, and the dates answer "is
                  this current?" — both questions come before reading. */}
              <div className="fx-noteview-meta">
                <span className="fx-noteview-meta-item">
                  <Icon d={I.clock} size={12} /> {t('note_view.updated', { when: relativeTime(note.updated_at, t) })}
                </span>
                <span className="fx-noteview-meta-item">
                  <Icon d={I.eye} size={12} /> {t('note_view.views', { count: note.click_count })}
                </span>
              </div>

              {note.tags.length > 0 && (
                <div className="fx-noteview-tags">
                  {note.tags.map((tag) => (
                    <TagChip key={tag.id} tag={tag} />
                  ))}
                </div>
              )}

              {/* Safe because the server sanitizes on every write path — see the
                  component doc and CLAUDE.md §4. Never render an unsanitized
                  string here. */}
              <article
                className="fx-noteview-content"
                dangerouslySetInnerHTML={{ __html: note.body_html }}
              />
            </>
          )}
        </div>

        <footer className="fx-modal-foot">
          <button type="button" className="fx-confirm-btn" onClick={onClose}>
            {t('common.close')}
          </button>
          {note && (
            <>
              {/* The public page, for when a real visit IS what you want — this
                  is the path that records one. */}
              <a
                className="fx-confirm-btn"
                href={goNoteHref(note)}
                target="_blank"
                rel="noopener noreferrer"
              >
                <Icon d={I.arrowR} size={13} /> {t('note_view.open_public')}
              </a>
              <button
                type="button"
                className="fx-confirm-btn fx-confirm-btn-primary"
                onClick={() => onEdit(note)}
              >
                <Icon d={I.pen} size={13} /> {t('common.edit')}
              </button>
            </>
          )}
        </footer>
      </div>
    </div>
  )
}

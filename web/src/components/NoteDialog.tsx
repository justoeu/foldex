import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useNote } from '../api/notes'
import type { Note } from '../api/types'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { Icon, I } from './icons'
import { NoteDialogEditor } from './NoteDialogEditor'

export { buildImageUploadHandler, buildNoteEditorProps } from './useNoteDialogController'

type Props = {
  open: boolean
  noteId: number | null
  defaultFolderId?: number | null
  onClose: () => void
}

type LoadBoundaryProps = {
  failed: boolean
  retrying: boolean
  onRetry: () => void
  onClose: () => void
}

function NoteLoadBoundary({ failed, retrying, onRetry, onClose }: LoadBoundaryProps) {
  const { t } = useTranslation()
  return (
    <>
      <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
        {failed ? (
          <div role="alert" style={{ minHeight: 180, display: 'grid', placeItems: 'center', textAlign: 'center', color: 'var(--fx-danger)', padding: 24 }}>
            <div><Icon d={I.alert} size={18} /> <div style={{ marginTop: 8 }}>{t('note_dialog.load_error')}</div></div>
          </div>
        ) : (
          <div role="status" aria-live="polite" style={{ minHeight: 180, display: 'grid', placeItems: 'center', color: 'var(--fx-ink-3)' }}>
            <span><span className="fx-spinner" /> {t('note_dialog.loading')}</span>
          </div>
        )}
      </div>
      <footer className="fx-modal-foot">
        <button type="button" className="fx-confirm-btn" onClick={onClose}>{t('common.cancel')}</button>
        {failed && (
          <button type="button" className="fx-confirm-btn fx-confirm-btn-primary" onClick={onRetry} disabled={retrying}>
            <Icon d={I.refresh} size={13} /> {t('note_dialog.load_retry')}
          </button>
        )}
      </footer>
    </>
  )
}

type DialogContentProps = {
  noteId: number | null
  note: Note | null
  failed: boolean
  retrying: boolean
  retry: () => void
  defaultFolderId?: number | null
  onClose: () => void
}

function NoteDialogContent({ noteId, note, failed, retrying, retry, defaultFolderId, onClose }: DialogContentProps) {
  if (noteId != null && !note) {
    return <NoteLoadBoundary failed={failed} retrying={retrying} onRetry={retry} onClose={onClose} />
  }
  return <NoteDialogEditor key={noteId == null ? 'create' : `edit-${noteId}`} note={note} defaultFolderId={defaultFolderId} onClose={onClose} />
}

export function NoteDialog({ open, noteId, defaultFolderId, onClose }: Props) {
  const { t } = useTranslation()
  const noteQuery = useNote(open ? noteId : null)
  const loadedNote = noteId != null && noteQuery.data?.id === noteId ? noteQuery.data : null
  const dialogRef = useRef<HTMLDivElement>(null)
  const isEdit = noteId != null
  useEscape(onClose, open)
  useFocusTrap(dialogRef, open)

  if (!open) return null
  return (
    <div ref={dialogRef} className="fx-overlay fx-overlay-modal" role="dialog" aria-modal="true" aria-label={isEdit ? t('note_dialog.edit_title') : t('note_dialog.create_title')}>
      <div className="fx-modal" style={{ maxWidth: 720 }}>
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker fx-modal-kicker-note">
              <Icon d={I.note} size={12} /> {isEdit ? t('note_dialog.kicker_edit') : t('note_dialog.kicker_create')}
            </div>
            <h2 className="fx-modal-title">{isEdit ? t('note_dialog.edit_title') : t('note_dialog.create_title')}</h2>
          </div>
          <button type="button" className="fx-confirm-x" onClick={onClose} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>
        <NoteDialogContent
          noteId={noteId}
          note={loadedNote}
          failed={noteQuery.isError}
          retrying={noteQuery.isFetching}
          retry={() => { void noteQuery.refetch() }}
          defaultFolderId={defaultFolderId}
          onClose={onClose}
        />
      </div>
    </div>
  )
}

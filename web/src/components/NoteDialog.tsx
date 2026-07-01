import { useEffect, useMemo, useRef, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { useEditor, EditorContent } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import Image from '@tiptap/extension-image'
import Placeholder from '@tiptap/extension-placeholder'
import type { EditorView } from '@tiptap/pm/view'
import { Icon, I } from './icons'
import { FolderPicker } from './FolderPicker'
import { TagChip } from './TagChip'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useCreateNote, useUpdateNote, useNote, uploadNoteImage } from '../api/notes'
import { useCreateTag, useTags } from '../api/tags'
import type { Tag } from '../api/types'

type Props = {
  open: boolean
  // Edit mode is keyed by id, not a full Note object — the interleaved grid
  // only carries a body_text_snippet (Entry), not the full body_html, so the
  // dialog fetches the complete row itself via useNote when opened for edit.
  noteId: number | null
  // Default folder for a new note — preselects the picker when the user is
  // creating a note while inside a folder view. Ignored in edit mode.
  defaultFolderId?: number | null
  onClose: () => void
}

// Same pending-tag pattern as LinkDialog: real tags have id > 0, tags typed
// inline live with id === 0 until save, when they're created and attached.
type SelectedTag = Tag & { _pending?: boolean }

// Same palette LinkDialog uses for pending-tag color cycling — kept in sync
// so a tag queued from either dialog looks the same before it's saved.
const INLINE_PALETTE = [
  '#6366F1', '#3B82F6', '#0EA5E9', '#06B6D4', '#14B8A6', '#10B981', '#22C55E', '#84CC16',
  '#EAB308', '#F59E0B', '#F97316', '#EF4444', '#F43F5E', '#EC4899', '#D946EF', '#A855F7',
  '#8B5CF6', '#64748B', '#78716C', '#6B7280',
]

// Inserts an uploaded image at the current selection. Exported so tests can
// exercise the upload-and-insert logic without mounting the full ProseMirror
// DOM (jsdom doesn't implement enough of the contenteditable/selection APIs
// for Tiptap to run in a test environment).
export function buildImageUploadHandler(
  uploadFn: (file: File) => Promise<{ url: string }>,
  onError: (message: string) => void,
) {
  return (view: EditorView, file: File) => {
    uploadFn(file)
      .then(({ url }) => {
        const { schema } = view.state
        const node = schema.nodes.image.create({ src: url })
        view.dispatch(view.state.tr.replaceSelectionWith(node))
      })
      .catch(() => onError('upload_failed'))
  }
}

function slugifyClient(title: string): string {
  return title
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80)
    .replace(/-+$/g, '')
}

export function NoteDialog({ open, noteId, defaultFolderId, onClose }: Props) {
  const { t } = useTranslation()
  const { data: tags = [] } = useTags()
  const createTag = useCreateTag()
  const createNote = useCreateNote()
  const updateNote = useUpdateNote()
  const { data: note } = useNote(open ? noteId : null)

  const [title, setTitle] = useState('')
  const [slug, setSlug] = useState('')
  const [slugDirty, setSlugDirty] = useState(false)
  const [pinned, setPinned] = useState(false)
  const [folderId, setFolderId] = useState<number | null>(null)
  const [selected, setSelected] = useState<SelectedTag[]>([])
  const [tagFilter, setTagFilter] = useState('')
  const [tagPage, setTagPage] = useState(0)
  const [imgUploadError, setImgUploadError] = useState<string | null>(null)

  const handleUpload = useMemo(
    () => buildImageUploadHandler(uploadNoteImage, () => setImgUploadError(t('note_dialog.image_error_generic'))),
    [t],
  )

  const editor = useEditor(
    {
      extensions: [
        StarterKit.configure({ link: { openOnClick: false } }),
        Image,
        Placeholder.configure({ placeholder: t('note_dialog.body_placeholder') }),
      ],
      editorProps: {
        handlePaste: (view, event) => {
          const file = Array.from(event.clipboardData?.items ?? [])
            .find((item) => item.type.startsWith('image/'))
            ?.getAsFile()
          if (!file) return false
          event.preventDefault()
          handleUpload(view, file)
          return true
        },
        handleDrop: (view, event) => {
          const file = Array.from(event.dataTransfer?.files ?? []).find((f) => f.type.startsWith('image/'))
          if (!file) return false
          event.preventDefault()
          handleUpload(view, file)
          return true
        },
      },
    },
    [open],
  )

  useEffect(() => {
    if (!open) return
    setTitle(note?.title ?? '')
    setSlug(note?.slug ?? '')
    setSlugDirty(!!note?.slug)
    setPinned(note?.pinned ?? false)
    setFolderId(note?.folder_id ?? defaultFolderId ?? null)
    setSelected(note?.tags ?? [])
    setTagFilter('')
    setTagPage(0)
    setImgUploadError(null)
    // isInitialized guards a real race: this effect's deps include `note`,
    // which can update (edit mode's useNote resolving async) before Tiptap's
    // own mount effect has finished wiring the editor's internal
    // commandManager — calling .commands before that throws. `open` flipping
    // true recreates the editor (see useEditor deps above) fresh + initialized
    // on the next tick, so a skipped call here just means the upcoming
    // initialized render carries the right content instead.
    if (editor?.isInitialized) {
      editor.commands.setContent(note?.body_html ?? '')
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- editor is recreated per `open` (see useEditor deps above); including it would re-run on every keystroke.
  }, [open, note, defaultFolderId])

  useEffect(() => {
    if (slugDirty) return
    setSlug(slugifyClient(title))
  }, [title, slugDirty])

  useEscape(onClose, open)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, open)

  const available = useMemo(
    () => tags.filter((tag) => !selected.some((s) => s.id === tag.id)),
    [tags, selected],
  )
  const filteredAvailable = useMemo(
    () => (tagFilter ? available.filter((tag) => tag.name.toLowerCase().includes(tagFilter.toLowerCase())) : available),
    [available, tagFilter],
  )
  useEffect(() => {
    const lastPage = Math.max(0, Math.ceil(filteredAvailable.length / 7) - 1)
    if (tagPage > lastPage) setTagPage(lastPage)
  }, [filteredAvailable.length, tagPage])
  const canCreateFromFilter =
    tagFilter.trim().length > 0 &&
    !tags.some((tag) => tag.name.toLowerCase() === tagFilter.trim().toLowerCase()) &&
    !selected.some((s) => s.name.toLowerCase() === tagFilter.trim().toLowerCase())

  if (!open) return null

  const queueInlineTag = () => {
    const name = tagFilter.trim()
    if (!name) return
    setSelected([...selected, { id: 0, name, color: INLINE_PALETTE[0], icon: null, _pending: true }])
    setTagFilter('')
  }

  const cycleColor = (idx: number) => {
    setSelected(
      selected.map((tag, i) => {
        if (i !== idx || !tag._pending) return tag
        const cur = INLINE_PALETTE.indexOf(tag.color)
        return { ...tag, color: INLINE_PALETTE[(cur + 1) % INLINE_PALETTE.length] }
      }),
    )
  }

  const submit = async () => {
    const trimmedTitle = title.trim()
    if (!trimmedTitle) return

    const tagIds: number[] = []
    for (const tag of selected) {
      if (tag.id) {
        tagIds.push(tag.id)
      } else {
        const created = await createTag.mutateAsync({ name: tag.name, color: tag.color })
        tagIds.push(created.id)
      }
    }

    const bodyHtml = editor?.getHTML() ?? ''

    if (note) {
      const slugTrimmed = slug.trim()
      const slugPayload: { slug?: string | null } = {}
      if (slugDirty) {
        slugPayload.slug = slugTrimmed === '' ? null : slugTrimmed
      }
      await updateNote.mutateAsync({
        id: note.id,
        body: {
          title: trimmedTitle,
          body_html: bodyHtml,
          tag_ids: tagIds,
          pinned,
          folder_id: folderId,
          ...slugPayload,
        },
      })
    } else {
      const slugTrimmed = slug.trim()
      const createSlug: { slug?: string } = {}
      if (slugDirty && slugTrimmed !== '') {
        createSlug.slug = slugTrimmed
      }
      await createNote.mutateAsync({
        title: trimmedTitle,
        body_html: bodyHtml,
        tag_ids: tagIds,
        pinned,
        folder_id: folderId,
        ...createSlug,
      })
    }
    onClose()
  }

  const busy = createNote.isPending || updateNote.isPending || createTag.isPending
  const isEdit = noteId != null

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={isEdit ? t('note_dialog.edit_title') : t('note_dialog.create_title')}
    >
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

        <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
          <div className="fx-modal-col">
            <label className="fx-field">
              <span className="fx-field-label">{t('note_dialog.title_label')}</span>
              <div className="fx-input">
                <input
                  autoFocus
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  placeholder={t('note_dialog.title_placeholder')}
                  aria-label={t('common.title_aria')}
                />
              </div>
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('note_dialog.slug_label')}</span>
              <div className="fx-input">
                <span style={{ color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)', fontSize: 12, paddingRight: 4 }}>
                  /n/
                </span>
                <input
                  value={slug}
                  onChange={(e) => {
                    setSlug(e.target.value)
                    setSlugDirty(true)
                  }}
                  placeholder={slugifyClient(title) || 'my-note'}
                  aria-label={t('note_dialog.slug_aria')}
                  pattern="[a-z0-9]+(-[a-z0-9]+)*"
                  style={{ fontFamily: 'var(--fx-mono)' }}
                />
                {slugDirty && (
                  <button
                    type="button"
                    className="fx-iconbtn"
                    onClick={() => {
                      setSlug(slugifyClient(title))
                      setSlugDirty(false)
                    }}
                    data-tooltip={t('note_dialog.slug_reset_tooltip')}
                    aria-label={t('note_dialog.slug_reset_tooltip')}
                  >
                    <Icon d={I.refresh} size={13} />
                  </button>
                )}
              </div>
              <span className="fx-field-hint">{t('note_dialog.slug_hint')}</span>
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('note_dialog.body_label')}</span>
              <div className="fx-tiptap-wrap">
                <EditorContent editor={editor} className="fx-tiptap" />
              </div>
              {imgUploadError && (
                <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, marginTop: 4 }}>
                  <Icon d={I.alert} size={12} /> {imgUploadError}
                </div>
              )}
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('note_dialog.tags_label')}</span>
              <div className="fx-tagpicker">
                {selected.map((tag, i) => (
                  <TagChip
                    key={tag.id || `pending-${i}`}
                    tag={tag}
                    active
                    closable
                    onClick={tag._pending ? () => cycleColor(i) : undefined}
                    onClose={() => setSelected(selected.filter((_, j) => j !== i))}
                  />
                ))}
                <input
                  className="fx-tagpicker-input"
                  value={tagFilter}
                  onChange={(e) => { setTagFilter(e.target.value); setTagPage(0) }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && canCreateFromFilter) {
                      e.preventDefault()
                      queueInlineTag()
                    }
                  }}
                  placeholder={t('note_dialog.tags_search_placeholder')}
                  aria-label={t('common.tag_filter_aria')}
                />
              </div>
              {selected.some((tag) => tag._pending) && (
                <div className="fx-tag-hint">
                  <Trans i18nKey="link_dialog.pending_tag_color_hint_html" components={{ strong: <strong /> }} />
                </div>
              )}
              {(filteredAvailable.length > 0 || canCreateFromFilter) && (() => {
                const PAGE = 7
                const totalPages = Math.ceil(filteredAvailable.length / PAGE)
                const pageTags = filteredAvailable.slice(tagPage * PAGE, (tagPage + 1) * PAGE)
                return (
                  <div style={{ marginTop: 10 }}>
                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
                      <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)' }}>
                        {t('link_dialog.tags_registered_label')}
                      </span>
                      {totalPages > 1 && (
                        <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                          <button
                            type="button"
                            className="fx-iconbtn"
                            disabled={tagPage === 0}
                            onClick={() => setTagPage((p) => p - 1)}
                            aria-label={t('link_dialog.tags_page_prev_aria')}
                            style={{ width: 22, height: 22 }}
                          >
                            <Icon d={I.chevronLeft} size={12} />
                          </button>
                          <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)', minWidth: 32, textAlign: 'center' }}>
                            {tagPage + 1}/{totalPages}
                          </span>
                          <button
                            type="button"
                            className="fx-iconbtn"
                            disabled={tagPage >= totalPages - 1}
                            onClick={() => setTagPage((p) => p + 1)}
                            aria-label={t('link_dialog.tags_page_next_aria')}
                            style={{ width: 22, height: 22 }}
                          >
                            <Icon d={I.chevronRight} size={12} />
                          </button>
                        </div>
                      )}
                    </div>
                    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                      {pageTags.map((tag) => (
                        <TagChip
                          key={tag.id}
                          tag={tag}
                          onClick={() => {
                            setSelected([...selected, tag])
                            setTagFilter('')
                          }}
                        />
                      ))}
                      {canCreateFromFilter && (
                        <button type="button" className="fx-pillbtn" onClick={queueInlineTag} style={{ fontSize: 11 }}>
                          <Icon d={I.plus} size={11} /> {t('link_dialog.tags_create_inline', { name: tagFilter.trim() })}
                        </button>
                      )}
                    </div>
                  </div>
                )
              })()}
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('note_dialog.folder_label')}</span>
              <FolderPicker selected={folderId} onChange={setFolderId} parentId={defaultFolderId ?? null} />
            </label>

            <label className="fx-toggle-row">
              <input
                type="checkbox"
                checked={pinned}
                onChange={(e) => setPinned(e.target.checked)}
                aria-label={t('note_dialog.pinned_aria')}
              />
              <span className="fx-toggle-track">
                <span className="fx-toggle-knob" />
              </span>
              <span className="fx-toggle-label">
                <Icon d={I.pin} size={12} /> {t('note_dialog.pinned_label')}
                <span className="fx-toggle-hint">{t('note_dialog.pinned_hint')}</span>
              </span>
            </label>
          </div>
        </div>

        <footer className="fx-modal-foot">
          <button type="button" className="fx-btn fx-btn-ghost" onClick={onClose}>
            {t('common.cancel')}
          </button>
          <button
            type="button"
            className="fx-btn fx-btn-note"
            onClick={submit}
            disabled={!title.trim() || busy}
          >
            <Icon d={isEdit ? I.check : I.plus} size={14} />
            {isEdit ? t('note_dialog.submit_save') : t('note_dialog.submit_create')}
          </button>
        </footer>
      </div>
    </div>
  )
}

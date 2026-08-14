import { useEffect, useMemo, useState } from 'react'
import { useEditor } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import Image from '@tiptap/extension-image'
import Placeholder from '@tiptap/extension-placeholder'
import { TextStyle, Color, FontFamily } from '@tiptap/extension-text-style'
import TextAlign from '@tiptap/extension-text-align'
import type { EditorView } from '@tiptap/pm/view'
import { useTranslation } from 'react-i18next'
import { useCreateNote, useUpdateNote, uploadNoteImage } from '../api/notes'
import type { Note } from '../api/types'
import { apiErrorCode } from '../lib/apiError'
import { slugifyClient } from '../lib/slugify'
import {
  buildCreateNotePayload,
  buildUpdateNotePayload,
  type NoteDialogValues,
  type SelectedNoteTag,
} from './NoteDialogPayload'

export function buildImageUploadHandler(
  uploadFn: (file: File) => Promise<{ url: string }>,
  onError: (message: string) => void,
) {
  return (view: EditorView, file: File) => {
    uploadFn(file)
      .then(({ url }) => {
        const node = view.state.schema.nodes.image.create({ src: url })
        view.dispatch(view.state.tr.replaceSelectionWith(node))
      })
      .catch(() => onError('upload_failed'))
  }
}

type ImageUploadHandler = (view: EditorView, file: File) => void

export function buildNoteEditorProps(handleUpload: ImageUploadHandler) {
  return {
    handlePaste: (view: EditorView, event: ClipboardEvent) => {
      const file = Array.from(event.clipboardData?.items ?? [])
        .find((item) => item.type.startsWith('image/'))
        ?.getAsFile()
      if (!file) return false
      event.preventDefault()
      handleUpload(view, file)
      return true
    },
    handleDrop: (view: EditorView, event: DragEvent) => {
      const file = Array.from(event.dataTransfer?.files ?? []).find((item) => item.type.startsWith('image/'))
      if (!file) return false
      event.preventDefault()
      handleUpload(view, file)
      return true
    },
  }
}

function responseMessage(error: unknown) {
  return (error as { response?: { data?: { error?: { message?: string } } } })?.response?.data?.error?.message
}

function responseStatus(error: unknown) {
  return (error as { response?: { status?: number } })?.response?.status
}

type ControllerOptions = {
  note: Note | null
  defaultFolderId?: number | null
  onClose: () => void
}

export function useNoteDialogController({ note, defaultFolderId, onClose }: ControllerOptions) {
  const { t } = useTranslation()
  const [baselineNote] = useState(note)
  const [title, setTitle] = useState(baselineNote?.title ?? '')
  const [slug, setSlug] = useState(baselineNote?.slug ?? '')
  const [slugDirty, setSlugDirty] = useState(Boolean(baselineNote?.slug))
  const [pinned, setPinned] = useState(baselineNote?.pinned ?? false)
  const [folderId, setFolderId] = useState<number | null>(baselineNote?.folder_id ?? defaultFolderId ?? null)
  const [selectedTags, setSelectedTags] = useState<SelectedNoteTag[]>(baselineNote?.tags ?? [])
  const [imgUploadError, setImgUploadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const createNote = useCreateNote()
  const updateNote = useUpdateNote()

  const handleUpload = useMemo(
    () => buildImageUploadHandler(uploadNoteImage, () => setImgUploadError(t('note_dialog.image_error_generic'))),
    [t],
  )
  const editor = useEditor(
    {
      content: baselineNote?.body_html ?? '',
      extensions: [
        StarterKit.configure({ link: { openOnClick: false } }),
        Image,
        Placeholder.configure({ placeholder: t('note_dialog.body_placeholder') }),
        TextStyle,
        Color,
        FontFamily,
        TextAlign.configure({ types: ['heading', 'paragraph'] }),
      ],
      editorProps: buildNoteEditorProps(handleUpload),
    },
    [],
  )

  useEffect(() => {
    if (!slugDirty) setSlug(slugifyClient(title))
  }, [title, slugDirty])

  const values = (): NoteDialogValues => ({ title, slug, slugDirty, pinned, folderId, selectedTags })

  const submit = async () => {
    if (!title.trim()) return
    setSaveError(null)
    try {
      const bodyHtml = editor?.getHTML() ?? ''
      if (baselineNote) {
        await updateNote.mutateAsync({
          id: baselineNote.id,
          body: buildUpdateNotePayload(values(), bodyHtml, baselineNote.updated_at),
        })
      } else {
        await createNote.mutateAsync(buildCreateNotePayload(values(), bodyHtml))
      }
      onClose()
    } catch (error: unknown) {
      const code = apiErrorCode(error)
      if (code === 'tag_name_taken') return setSaveError(t('note_dialog.error_tag_taken'))
      if (code === 'conflict' || responseStatus(error) === 409) return setSaveError(t('note_dialog.error_conflict'))
      setSaveError(responseMessage(error) || t('note_dialog.error_generic'))
    }
  }

  return {
    editor,
    handleUpload,
    title,
    setTitle,
    slug,
    setSlug,
    slugDirty,
    setSlugDirty,
    pinned,
    setPinned,
    folderId,
    setFolderId,
    selectedTags,
    setSelectedTags,
    imgUploadError,
    saveError,
    busy: createNote.isPending || updateNote.isPending,
    isEdit: baselineNote != null,
    submit,
  }
}

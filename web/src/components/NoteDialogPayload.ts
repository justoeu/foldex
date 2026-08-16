import type { NoteCreate, NoteUpdate } from '../api/types'
import { dialogTagPayload, type SelectedTag } from '../lib/dialogTags'
import { createSlugValue, updateSlugValue } from '../lib/slugValue'

export type SelectedNoteTag = SelectedTag

export type NoteDialogValues = {
  title: string
  slug: string
  slugDirty: boolean
  pinned: boolean
  folderId: number | null
  selectedTags: SelectedNoteTag[]
}

export function buildCreateNotePayload(values: NoteDialogValues, bodyHtml: string): NoteCreate {
  const payload: NoteCreate = {
    title: values.title.trim(),
    body_html: bodyHtml,
    ...dialogTagPayload(values.selectedTags),
    pinned: values.pinned,
    folder_id: values.folderId,
  }
  const slug = createSlugValue(values)
  if (slug !== undefined) payload.slug = slug
  return payload
}

export function buildUpdateNotePayload(
  values: NoteDialogValues,
  bodyHtml: string,
  updatedAt: string,
): NoteUpdate {
  const payload: NoteUpdate = {
    if_match_updated_at: updatedAt,
    title: values.title.trim(),
    body_html: bodyHtml,
    ...dialogTagPayload(values.selectedTags),
    pinned: values.pinned,
    folder_id: values.folderId,
  }
  const slug = updateSlugValue(values)
  if (slug !== undefined) payload.slug = slug
  return payload
}

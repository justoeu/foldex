import type { NoteCreate, NoteUpdate, Tag } from '../api/types'

export type SelectedNoteTag = Tag & { _pending?: boolean }

export type NoteDialogValues = {
  title: string
  slug: string
  slugDirty: boolean
  pinned: boolean
  folderId: number | null
  selectedTags: SelectedNoteTag[]
}

function tagPayload(selectedTags: SelectedNoteTag[]) {
  return {
    tag_ids: selectedTags.filter((tag) => tag.id > 0).map((tag) => tag.id),
    pending_tags: selectedTags
      .filter((tag) => tag.id === 0)
      .map(({ name, color, icon }) => ({ name, color, icon })),
  }
}

export function buildCreateNotePayload(values: NoteDialogValues, bodyHtml: string): NoteCreate {
  const payload: NoteCreate = {
    title: values.title.trim(),
    body_html: bodyHtml,
    ...tagPayload(values.selectedTags),
    pinned: values.pinned,
    folder_id: values.folderId,
  }
  const slug = values.slug.trim()
  if (values.slugDirty && slug) payload.slug = slug
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
    ...tagPayload(values.selectedTags),
    pinned: values.pinned,
    folder_id: values.folderId,
  }
  if (values.slugDirty) payload.slug = values.slug.trim() || null
  return payload
}

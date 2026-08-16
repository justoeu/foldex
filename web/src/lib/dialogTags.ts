import type { Tag, TagCreate } from '../api/types'
import { apiErrorCode } from './apiError'

export type SelectedTag = Tag & { _pending?: boolean }

export type DialogTagPayload = {
  tag_ids: number[]
  pending_tags: TagCreate[]
}

export function dialogTagPayload(selected: SelectedTag[]): DialogTagPayload {
  return {
    tag_ids: selected.filter((tag) => tag.id > 0).map((tag) => tag.id),
    pending_tags: selected
      .filter((tag) => tag.id === 0)
      .map(({ name, color, icon }) => ({ name, color, icon })),
  }
}

const tagNameTakenKeys = {
  link: 'link_dialog.error_tag_taken',
  note: 'note_dialog.error_tag_taken',
} as const

export type DialogTagHost = keyof typeof tagNameTakenKeys
export type TagNameTakenKey = typeof tagNameTakenKeys[DialogTagHost]

export function tagNameTakenErrorKey(error: unknown, host: DialogTagHost): TagNameTakenKey | null {
  return apiErrorCode(error) === 'tag_name_taken' ? tagNameTakenKeys[host] : null
}

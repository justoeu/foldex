import type { Link, LinkCreate, LinkUpdate } from '../api/types'
import { dialogTagPayload, type SelectedTag } from './dialogTags'
import { createSlugValue, updateSlugValue } from './slugValue'
import type { CheckInterval } from './time'

export type LinkDialogValues = {
  url: string
  title: string
  slug: string
  slugDirty: boolean
  description: string
  pinned: boolean
  folderId: number | null
  checkInterval: CheckInterval | null
}

export function buildLinkCreatePayload(
  values: LinkDialogValues,
  selected: SelectedTag[],
): LinkCreate {
  const url = values.url.trim()
  const body: LinkCreate = {
    url,
    title: values.title.trim() || url,
    description: values.description.trim() || null,
    ...dialogTagPayload(selected),
    pinned: values.pinned,
    folder_id: values.folderId,
    check_interval: values.checkInterval,
  }
  const slug = createSlugValue(values)
  if (slug !== undefined) body.slug = slug
  return body
}

export function buildLinkUpdatePayload(
  link: Link,
  values: LinkDialogValues,
  selected: SelectedTag[],
): LinkUpdate {
  const url = values.url.trim()
  const body: LinkUpdate = {
    if_match_updated_at: link.updated_at,
    url,
    title: values.title.trim() || url,
    description: values.description.trim() || null,
    ...dialogTagPayload(selected),
    pinned: values.pinned,
    folder_id: values.folderId,
    check_interval: values.checkInterval,
  }
  const slug = updateSlugValue(values)
  if (slug !== undefined) body.slug = slug
  return body
}

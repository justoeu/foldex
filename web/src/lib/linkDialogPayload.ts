import type { Link, LinkCreate, LinkUpdate, Tag, TagCreate } from '../api/types'
import type { CheckInterval } from './time'

export type SelectedTag = Tag & { _pending?: boolean }

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

export function splitLinkTags(selected: SelectedTag[]): {
  tagIds: number[]
  pendingTags: TagCreate[]
} {
  return {
    tagIds: selected.filter((tag) => tag.id > 0).map((tag) => tag.id),
    pendingTags: selected
      .filter((tag) => tag.id === 0)
      .map(({ name, color, icon }) => ({ name, color, icon })),
  }
}

export function buildLinkCreatePayload(
  values: LinkDialogValues,
  selected: SelectedTag[],
): LinkCreate {
  const url = values.url.trim()
  const tags = splitLinkTags(selected)
  const body: LinkCreate = {
    url,
    title: values.title.trim() || url,
    description: values.description.trim() || null,
    tag_ids: tags.tagIds,
    pending_tags: tags.pendingTags,
    pinned: values.pinned,
    folder_id: values.folderId,
    check_interval: values.checkInterval,
  }
  const slug = values.slug.trim()
  if (values.slugDirty && slug) body.slug = slug
  return body
}

export function buildLinkUpdatePayload(
  link: Link,
  values: LinkDialogValues,
  selected: SelectedTag[],
): LinkUpdate {
  const url = values.url.trim()
  const tags = splitLinkTags(selected)
  const body: LinkUpdate = {
    if_match_updated_at: link.updated_at,
    url,
    title: values.title.trim() || url,
    description: values.description.trim() || null,
    tag_ids: tags.tagIds,
    pending_tags: tags.pendingTags,
    pinned: values.pinned,
    folder_id: values.folderId,
    check_interval: values.checkInterval,
  }
  if (values.slugDirty) body.slug = values.slug.trim() || null
  return body
}

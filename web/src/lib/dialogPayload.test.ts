import { describe, expect, it } from 'vitest'
import type { Link } from '../api/types'
import { buildCreateNotePayload, buildUpdateNotePayload, type NoteDialogValues } from '../components/NoteDialogPayload'
import { buildLinkCreatePayload, buildLinkUpdatePayload, type LinkDialogValues } from './linkDialogPayload'

type Payload = { slug?: string | null }
type SlugInput = { slug: string; slugDirty: boolean }

const link: Link = {
  id: 1,
  url: 'https://example.com',
  title: 'Example',
  slug: 'example',
  click_count: 0,
  preview_status: 'ok',
  pinned: false,
  created_at: 'created',
  updated_at: 'version-1',
  tags: [],
}

const hosts: Array<{
  name: string
  create: (slug: SlugInput) => Payload
  update: (slug: SlugInput) => Payload
}> = [
  {
    name: 'link',
    create: (slug) => buildLinkCreatePayload(linkValues(slug), []),
    update: (slug) => buildLinkUpdatePayload(link, linkValues(slug), []),
  },
  {
    name: 'note',
    create: (slug) => buildCreateNotePayload(noteValues(slug), '<p>body</p>'),
    update: (slug) => buildUpdateNotePayload(noteValues(slug), '<p>body</p>', 'version-1'),
  },
]

function linkValues(slug: SlugInput): LinkDialogValues {
  return {
    url: ' https://example.com ',
    title: ' Example ',
    description: '',
    pinned: false,
    folderId: null,
    checkInterval: null,
    ...slug,
  }
}

function noteValues(slug: SlugInput): NoteDialogValues {
  return {
    title: ' Example ',
    pinned: false,
    folderId: null,
    selectedTags: [],
    ...slug,
  }
}

describe.each(hosts)('$name dialog slug payloads', ({ create, update }) => {
  it.each([
    ['untouched derived value', { slug: 'derived', slugDirty: false }, undefined],
    ['explicit custom value', { slug: ' custom ', slugDirty: true }, 'custom'],
    ['explicit empty value', { slug: ' ', slugDirty: true }, undefined],
  ] as const)('creates with %s', (_name, slug, expected) => {
    const payload = create(slug)
    if (expected === undefined) expect(payload).not.toHaveProperty('slug')
    else expect(payload.slug).toBe(expected)
  })

  it.each([
    ['untouched derived value', { slug: 'derived', slugDirty: false }, undefined],
    ['explicit custom value', { slug: ' custom ', slugDirty: true }, 'custom'],
    ['explicit empty value', { slug: ' ', slugDirty: true }, null],
  ] as const)('updates with %s', (_name, slug, expected) => {
    const payload = update(slug)
    if (expected === undefined) expect(payload).not.toHaveProperty('slug')
    else expect(payload.slug).toBe(expected)
  })
})

import { memo, useCallback, useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { TFunction } from 'i18next'
import type { Entry } from '../api/types'
import { renderWithProviders } from '../test/renderWithProviders'

const metrics = vi.hoisted(() => ({
  linkRenders: new Map<number, number>(),
  noteRenders: new Map<number, number>(),
  pinLinkHookCalls: 0,
  pinNoteHookCalls: 0,
}))

vi.mock('./LinkCard', () => ({
  LinkCard: memo(({ link, onPin }: { link: Extract<Entry, { kind: 'link' }>; onPin: (link: Extract<Entry, { kind: 'link' }>, pinned: boolean) => void }) => {
    metrics.linkRenders.set(link.id, (metrics.linkRenders.get(link.id) ?? 0) + 1)
    return <button onClick={() => onPin(link, !link.pinned)}>pin-link-{link.id}</button>
  }),
}))

vi.mock('./NoteCard', () => ({
  NoteCard: memo(({ note, onPin }: { note: Extract<Entry, { kind: 'note' }>; onPin: (note: Extract<Entry, { kind: 'note' }>, pinned: boolean) => void }) => {
    metrics.noteRenders.set(note.id, (metrics.noteRenders.get(note.id) ?? 0) + 1)
    return <button onClick={() => onPin(note, !note.pinned)}>pin-note-{note.id}</button>
  }),
}))

vi.mock('../api/links', async () => {
  const stableMutation = () => {
    const [, setVersion] = useState(0)
    const mutate = useCallback(() => setVersion((value) => value + 1), [])
    return { mutate }
  }
  return {
    useDeleteLink: stableMutation,
    usePinLink: () => {
      metrics.pinLinkHookCalls += 1
      return stableMutation()
    },
    useRefreshPreview: stableMutation,
    useMarkChangeSeen: stableMutation,
  }
})

vi.mock('../api/notes', () => {
  const stableMutation = () => {
    const [, setVersion] = useState(0)
    const mutate = useCallback(() => setVersion((value) => value + 1), [])
    return { mutate }
  }
  return {
    useDeleteNote: stableMutation,
    usePinNote: () => {
      metrics.pinNoteHookCalls += 1
      return stableMutation()
    },
  }
})

import { CardsView } from './HomeView'

const link = (id: number): Extract<Entry, { kind: 'link' }> => ({
  kind: 'link', id, url: `https://${id}.example`, title: `Link ${id}`, slug: `link-${id}`,
  click_count: 0, preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
})

const note = (id: number): Extract<Entry, { kind: 'note' }> => ({
  kind: 'note', id, title: `Note ${id}`, slug: `note-${id}`, click_count: 0,
  pinned: false, created_at: '', updated_at: '', tags: [],
})

describe('CardsView mutation wiring', () => {
  it('mounts mutation observers once and keeps sibling card props stable during mutation state changes', async () => {
    metrics.linkRenders.clear()
    metrics.noteRenders.clear()
    metrics.pinLinkHookCalls = 0
    metrics.pinNoteHookCalls = 0
    const entries = [link(1), link(2), note(3), note(4)]
    const noop = () => undefined
    const t = ((key: string) => key) as TFunction
    const props = {
      folders: [],
      sort: 'created' as const,
      isLoading: false,
      foldersCompact: false,
      onEdit: noop,
      onEditNote: noop,
      onOpenFolder: noop,
      onEditFolder: noop,
      onMoveLinkToFolder: noop,
      onMoveNoteToFolder: noop,
      onMergeEntries: noop,
      onMoveFolder: noop,
      t,
    }

    const view = renderWithProviders(
      <CardsView
        {...props}
        entries={entries}
      />,
    )

    expect(metrics.pinLinkHookCalls).toBe(1)
    expect(metrics.pinNoteHookCalls).toBe(1)
    expect([...metrics.linkRenders.values()]).toEqual([1, 1])
    expect([...metrics.noteRenders.values()]).toEqual([1, 1])

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'pin-link-1' }))
    await user.click(screen.getByRole('button', { name: 'pin-note-3' }))

    expect([...metrics.linkRenders.values()]).toEqual([1, 1])
    expect([...metrics.noteRenders.values()]).toEqual([1, 1])

    view.rerender(<CardsView {...props} entries={[{ ...entries[0], pinned: true } as Entry, ...entries.slice(1)]} />)
    expect([...metrics.linkRenders.values()]).toEqual([2, 1])
    expect([...metrics.noteRenders.values()]).toEqual([1, 1])
  })
})

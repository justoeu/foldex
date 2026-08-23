import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from '../api/client'
import { INLINE_PALETTE } from '../lib/inlinePalette'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { renderWithProviders } from '../test/renderWithProviders'
import { LinkDialog } from './LinkDialog'
import { NoteDialog } from './NoteDialog'

// A pending chip's colour is SUGGESTED now (a palette entry nothing else is
// using), so pinning an index would assert the old fixed default rather than
// the payload shape these tests are about.
const paletteRe = new RegExp(`^(${INLINE_PALETTE.join('|')})$`)


let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('dialog deferred-tag parity', () => {
  it.each(['link', 'note'] as const)(
    '%s create preserves a rejected pending tag and can retry',
    async (host) => {
      const endpoint = host === 'link' ? '/api/links' : '/api/notes'
      const conflict = Object.assign(new Error('tag conflict'), {
        response: { status: 409, data: { error: { code: 'tag_name_taken' } } },
      })
      vi.mocked(http.post).mockRejectedValueOnce(conflict)
      const onClose = vi.fn()
      if (host === 'link') {
        renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
      } else {
        renderWithProviders(<NoteDialog open noteId={null} onClose={onClose} />)
      }
      const user = userEvent.setup()
      if (host === 'link') {
        await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://tag-conflict.example')
      } else {
        await user.type(screen.getByPlaceholderText('Give your note a title…'), 'Tag conflict')
      }
      await user.type(screen.getByLabelText('tag filter'), 'duplicate{Enter}')

      await user.click(screen.getByRole('button', {
        name: host === 'link' ? /Save link/i : /Create note/i,
      }))

      expect(await screen.findByRole('alert')).toHaveTextContent('A tag with this name already exists.')
      expect(screen.getByText('duplicate')).toBeInTheDocument()
      expect(screen.getByLabelText('tag filter')).toBeInTheDocument()
      expect(onClose).not.toHaveBeenCalled()

      await user.click(screen.getByRole('button', {
        name: host === 'link' ? /Save link/i : /Create note/i,
      }))

      await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
      const parentCalls = vi.mocked(http.post).mock.calls.filter(([url]) => url === endpoint)
      expect(parentCalls).toHaveLength(2)
      expect(parentCalls[0]?.[1]).toEqual(expect.objectContaining({
        tag_ids: [],
        pending_tags: [{ name: 'duplicate', color: expect.stringMatching(paletteRe), icon: null }],
      }))
      expect(parentCalls[1]?.[1]).toEqual(expect.objectContaining({
        tag_ids: [],
        pending_tags: [{ name: 'duplicate', color: expect.stringMatching(paletteRe), icon: null }],
      }))
    },
  )
})

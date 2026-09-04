import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { useExistingLinkByURL } from './useExistingLinkByURL'
import { makeQueryClient } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import type { ReactNode } from 'react'

let state: MockState

beforeEach(() => {
  state = freshState()
  state.links.push({
    id: 4, url: 'https://dup.example', title: 'Already', slug: 'already',
    click_count: 0, preview_status: 'ok', pinned: false, folder_id: 9,
    created_at: '', updated_at: '', tags: [],
  } as MockState['links'][number])
  installAxiosMock(state)
})

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={makeQueryClient()}>{children}</QueryClientProvider>
}

describe('useExistingLinkByURL', () => {
  it('returns the owner-scoped row after the debounce', async () => {
    const { result } = renderHook(
      () => useExistingLinkByURL('https://dup.example', true),
      { wrapper },
    )
    expect(result.current.existing).toBeNull()
    await waitFor(() => expect(result.current.existing?.id).toBe(4))
    expect(result.current.existing?.folder_id).toBe(9)
  })

  it('ignores the row currently being edited', async () => {
    const { result } = renderHook(
      () => useExistingLinkByURL('https://dup.example', true, 4),
      { wrapper },
    )
    await new Promise((resolve) => setTimeout(resolve, 800))
    expect(result.current.existing).toBeNull()
    expect(result.current.pending).toBe(false)
  })

  it('is empty when the URL is free', async () => {
    const { result } = renderHook(
      () => useExistingLinkByURL('https://new.example', true),
      { wrapper },
    )
    await new Promise((resolve) => setTimeout(resolve, 800))
    expect(result.current.existing).toBeNull()
    expect(result.current.pending).toBe(false)
  })
})

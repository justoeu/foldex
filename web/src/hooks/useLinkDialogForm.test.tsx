import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useLinkDialogForm } from './useLinkDialogForm'
import type { Link } from '../api/types'

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

const homeLink = {
  id: 7,
  url: 'https://home.example',
  title: 'A Home link',
  folder_id: null,
  pinned: false,
  click_count: 0,
  preview_status: 'ok',
  created_at: '',
  updated_at: '',
  tags: [],
} as unknown as Link

const folderedLink = { ...homeLink, id: 8, folder_id: 9 } as unknown as Link

// Regression (silent move): editing an existing link must seed the form from
// the LINK's own folder, not the ambient folder context. `folder_id: null` is
// a legitimate value ("lives on Home"), so a nullish-coalescing fallback to
// defaultFolderId made every palette edit of an ungrouped link — opened while
// a folder was on screen — PATCH the link into that folder on save.
describe('useLinkDialogForm folder seeding', () => {
  it('edit mode keeps a Home link on Home even when a folder is open (folder_id stays null)', () => {
    const { result } = renderHook(() => useLinkDialogForm(true, homeLink, undefined, 42), { wrapper })
    expect(result.current.folderId).toBeNull()
  })

  it('edit mode keeps the link own folder, ignoring the ambient default', () => {
    const { result } = renderHook(() => useLinkDialogForm(true, folderedLink, undefined, 42), { wrapper })
    expect(result.current.folderId).toBe(9)
  })

  it('create mode still seeds the current folder as the default', () => {
    const { result } = renderHook(() => useLinkDialogForm(true, null, undefined, 42), { wrapper })
    expect(result.current.folderId).toBe(42)
  })
})

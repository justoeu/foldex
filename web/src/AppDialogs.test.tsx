import { describe, expect, it } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useAppDialogController } from './AppDialogs'
import type { Link } from './api/types'

const link = { id: 7, title: 'a link' } as Link

describe('useAppDialogController — where the link dialog lands', () => {
  it('opens on the URL field from the pencil and on the image panel from the card', () => {
    const { result } = renderHook(() => useAppDialogController(undefined))

    act(() => result.current.openEditLink(link))
    expect(result.current.linkDialogOpen).toBe(true)
    expect(result.current.editLink).toBe(link)
    expect(result.current.linkFocus).toBe('url')

    act(() => result.current.closeLink())
    act(() => result.current.openLinkImage(link))
    expect(result.current.editLink).toBe(link)
    expect(result.current.linkFocus).toBe('image')
  })

  // The reset is the whole point of this test. Without it the intent survives
  // the close, and the next Alt+N lands on an upload zone belonging to a link
  // that has no id yet — a dialog focused on a control it cannot use.
  it('forgets the image intent on close, so the next new-link opens on the URL', () => {
    const { result } = renderHook(() => useAppDialogController(undefined))

    act(() => result.current.openLinkImage(link))
    expect(result.current.linkFocus).toBe('image')

    act(() => result.current.closeLink())
    expect(result.current.linkDialogOpen).toBe(false)

    act(() => result.current.openNewLink())
    expect(result.current.editLink).toBeNull()
    expect(result.current.linkFocus).toBe('url')
  })

  it('opens a blank dialog on the URL field even straight after an image open', () => {
    const { result } = renderHook(() => useAppDialogController(undefined))

    act(() => result.current.openLinkImage(link))
    act(() => result.current.openNewLink())
    expect(result.current.linkFocus).toBe('url')
    expect(result.current.editLink).toBeNull()
  })
})

import { describe, expect, it, vi } from 'vitest'
import { createRef } from 'react'
import { renderHook, waitFor } from '@testing-library/react'
import { useDialogInitialFocus } from './useDialogInitialFocus'

function setup(scroll: boolean) {
  const dialog = document.createElement('div')
  const target = document.createElement('input')
  dialog.appendChild(target)
  document.body.appendChild(dialog)

  const focus = vi.spyOn(target, 'focus')
  // jsdom has no scrollIntoView at all, so there is nothing to spy on —
  // assigning it is what makes the call observable, and its ABSENCE is the
  // other half of the contract (the hook calls it optionally on purpose).
  const scrollIntoView = vi.fn()
  ;(target as unknown as { scrollIntoView: () => void }).scrollIntoView = scrollIntoView

  const dialogRef = createRef<HTMLElement>() as React.RefObject<HTMLElement | null>
  const targetRef = createRef<HTMLElement>() as React.RefObject<HTMLElement | null>
  dialogRef.current = dialog
  targetRef.current = target

  const view = renderHook(() => useDialogInitialFocus(true, dialogRef, targetRef, scroll))
  return { focus, scrollIntoView, target, unmount: view.unmount }
}

describe('useDialogInitialFocus', () => {
  it('suppresses scrolling by default — the URL field is already at the top', async () => {
    const { focus, scrollIntoView } = setup(false)
    await waitFor(() => expect(focus).toHaveBeenCalled())
    expect(focus).toHaveBeenCalledWith({ preventScroll: true })
    expect(scrollIntoView).not.toHaveBeenCalled()
  })

  // Not cosmetic: the image panel stacks below the fold on a narrow viewport
  // (INV-165), so focusing without scrolling leaves the reader staring at the
  // form they did not ask for while the focused control sits off-screen.
  it('scrolls the target into view when asked', async () => {
    const { focus, scrollIntoView } = setup(true)
    await waitFor(() => expect(focus).toHaveBeenCalled())
    expect(focus).toHaveBeenCalledWith({ preventScroll: false })
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'center' })
  })

  it('leaves focus alone when something inside the dialog already claimed it', async () => {
    const dialog = document.createElement('div')
    const claimed = document.createElement('input')
    const target = document.createElement('input')
    dialog.append(claimed, target)
    document.body.appendChild(dialog)
    claimed.focus()

    const focus = vi.spyOn(target, 'focus')
    const dialogRef = { current: dialog as HTMLElement | null }
    const targetRef = { current: target as HTMLElement | null }

    renderHook(() => useDialogInitialFocus(true, dialogRef, targetRef, false))
    await new Promise((resolve) => requestAnimationFrame(resolve))
    expect(focus).not.toHaveBeenCalled()
    expect(claimed).toHaveFocus()
  })

  it('does nothing while the dialog is closed', async () => {
    const target = document.createElement('input')
    document.body.appendChild(target)
    const focus = vi.spyOn(target, 'focus')

    renderHook(() =>
      useDialogInitialFocus(false, { current: null }, { current: target as HTMLElement | null }, false),
    )
    await new Promise((resolve) => requestAnimationFrame(resolve))
    expect(focus).not.toHaveBeenCalled()
  })
})

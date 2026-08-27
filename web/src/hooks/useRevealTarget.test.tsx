import { describe, expect, it, vi, afterEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { useRevealTarget } from './useRevealTarget'

const realMatchMedia = window.matchMedia

afterEach(() => {
  Object.defineProperty(window, 'matchMedia', { writable: true, value: realMatchMedia })
  vi.restoreAllMocks()
})

/** Pretends the viewer asked their OS to stop animating things. */
function prefersReducedMotion(reduce: boolean) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn((query: string) => ({ matches: reduce, media: query })),
  })
}

/** Waits one animation frame — the unit this hook actually schedules on. */
function nextFrame() {
  return new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
}

function setup() {
  const el = document.createElement('div')
  document.body.appendChild(el)
  const focus = vi.spyOn(el, 'focus')
  // jsdom implements no scrollIntoView at all, so there is nothing to spy on:
  // assigning it is what makes the call observable, and its ABSENCE is the
  // other half of the contract (the hook calls it optionally on purpose).
  const scrollIntoView = vi.fn()
  ;(el as unknown as { scrollIntoView: () => void }).scrollIntoView = scrollIntoView

  const view = renderHook(() => useRevealTarget<HTMLDivElement>())
  view.result.current.ref.current = el
  return { el, focus, scrollIntoView, view }
}

describe('useRevealTarget', () => {
  it('defers to the next frame — a reveal fired during render must not fight the commit', async () => {
    const { focus, view } = setup()
    view.result.current.reveal()
    expect(focus).not.toHaveBeenCalled()
    await waitFor(() => expect(focus).toHaveBeenCalled())
  })

  it('focuses without scrolling, then centres the card itself', async () => {
    prefersReducedMotion(false)
    const { focus, scrollIntoView, view } = setup()
    view.result.current.reveal()

    await waitFor(() => expect(focus).toHaveBeenCalled())
    // preventScroll on purpose: the scroll is the NEXT line's job, and letting
    // focus do it too lands the card at the top of the viewport instead of its
    // middle.
    expect(focus).toHaveBeenCalledWith({ preventScroll: true })
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'center', behavior: 'smooth' })
  })

  it('jumps instead of gliding when the viewer asked for reduced motion', async () => {
    prefersReducedMotion(true)
    const { scrollIntoView, view } = setup()
    view.result.current.reveal()
    await waitFor(() => expect(scrollIntoView).toHaveBeenCalled())
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'center', behavior: 'auto' })
  })

  it('survives a host with neither matchMedia nor scrollIntoView', async () => {
    Object.defineProperty(window, 'matchMedia', { writable: true, value: undefined })
    const el = document.createElement('div')
    document.body.appendChild(el)
    const focus = vi.spyOn(el, 'focus')

    const view = renderHook(() => useRevealTarget<HTMLDivElement>())
    view.result.current.ref.current = el
    view.result.current.reveal()

    // A throw inside a requestAnimationFrame callback is uncaught and takes
    // the frame with it, so the guards are what keep focus — the part that
    // matters — from being lost with it.
    await waitFor(() => expect(focus).toHaveBeenCalledWith({ preventScroll: true }))
  })

  it('does nothing when the ref never landed on an element', async () => {
    const view = renderHook(() => useRevealTarget<HTMLDivElement>())
    expect(() => view.result.current.reveal()).not.toThrow()
    await new Promise((resolve) => requestAnimationFrame(resolve))
  })

  it('cancels a pending frame on unmount', async () => {
    const cancel = vi.spyOn(window, 'cancelAnimationFrame')
    const { focus, view } = setup()
    view.result.current.reveal()
    view.unmount()

    expect(cancel).toHaveBeenCalled()
    // A frame scheduled AFTER the hook's runs after it would have: waiting for
    // ours is what proves the hook's never fired, without a wall-clock sleep
    // that would only be flake dressed up as a wait.
    await nextFrame()
    expect(focus).not.toHaveBeenCalled()
  })

  it('coalesces two reveals in one frame into the single one that lands', async () => {
    const { focus, scrollIntoView, view } = setup()
    // Two clicks inside one frame — a job card and then another before the
    // browser painted. Two live frames would focus twice and scroll twice,
    // and the first scroll is toward a card the second click already moved.
    view.result.current.reveal()
    view.result.current.reveal()

    await waitFor(() => expect(focus).toHaveBeenCalled())
    await nextFrame()
    expect(focus).toHaveBeenCalledTimes(1)
    expect(scrollIntoView).toHaveBeenCalledTimes(1)
  })
})

import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { useRevealEntry, type RevealTarget } from './useRevealEntry'

function mountTarget(id = 7) {
  const el = document.createElement('article')
  el.setAttribute('data-entry', `link-${id}`)
  el.tabIndex = -1
  const scrollIntoView = vi.fn()
  ;(el as unknown as { scrollIntoView: typeof scrollIntoView }).scrollIntoView = scrollIntoView
  const focus = vi.spyOn(el, 'focus')
  document.body.appendChild(el)
  return { el, scrollIntoView, focus }
}

describe('useRevealEntry', () => {
  afterEach(() => {
    document.body.replaceChildren()
    vi.useRealTimers()
  })

  it('focuses, scrolls, and highlights once the node is ready', async () => {
    const { el, scrollIntoView, focus } = mountTarget()
    const onRevealed = vi.fn()
    const target: RevealTarget = { kind: 'link', id: 7 }
    renderHook(() => useRevealEntry(target, true, onRevealed))
    await waitFor(() => expect(scrollIntoView).toHaveBeenCalled())
    expect(focus).toHaveBeenCalledWith({ preventScroll: true })
    expect(el).toHaveClass('fx-entry-reveal')
    expect(onRevealed).not.toHaveBeenCalled()
  })

  it('waits until ready before querying the node', async () => {
    const { scrollIntoView } = mountTarget()
    const onRevealed = vi.fn()
    const target: RevealTarget = { kind: 'link', id: 7 }
    const view = renderHook(
      ({ ready }: { ready: boolean }) => useRevealEntry(target, ready, onRevealed),
      { initialProps: { ready: false } },
    )
    expect(scrollIntoView).not.toHaveBeenCalled()
    view.rerender({ ready: true })
    await waitFor(() => expect(scrollIntoView).toHaveBeenCalled())
  })

  it('does not notify immediately when the node is missing', () => {
    const onRevealed = vi.fn()
    renderHook(() => useRevealEntry({ kind: 'link', id: 99 }, true, onRevealed))
    expect(onRevealed).not.toHaveBeenCalled()
  })

  it('gives up if the node never appears', () => {
    vi.useFakeTimers()
    const onRevealed = vi.fn()
    renderHook(() => useRevealEntry({ kind: 'link', id: 99 }, false, onRevealed))
    act(() => {
      vi.advanceTimersByTime(3999)
    })
    expect(onRevealed).not.toHaveBeenCalled()
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(onRevealed).toHaveBeenCalledTimes(1)
  })

  it('clears the highlight and notifies after the pulse', async () => {
    vi.useFakeTimers()
    const { el } = mountTarget()
    const onRevealed = vi.fn()
    renderHook(() => useRevealEntry({ kind: 'link', id: 7 }, true, onRevealed))
    expect(el).toHaveClass('fx-entry-reveal')
    act(() => {
      vi.advanceTimersByTime(1400)
    })
    expect(el).not.toHaveClass('fx-entry-reveal')
    expect(onRevealed).toHaveBeenCalledTimes(1)
  })
})

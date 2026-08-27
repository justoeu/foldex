import { useCallback, useEffect, useRef } from 'react'

/**
 * Brings a section of the page to the reader — focus first, then the viewport.
 *
 * The pair matters. Focus alone moves the keyboard caret to a card that may be
 * off-screen; a scroll alone moves the page while the caret stays where the
 * click left it, so the next Tab jumps back out. `preventScroll` is what keeps
 * the two from fighting: the browser's own focus scroll lands the element at
 * the edge of the viewport, and the explicit `block: 'center'` below is the
 * position this is for.
 *
 * The deferral is not decoration either — a reveal is fired from a click
 * handler that also sets state, so the element being revealed may not have
 * rendered its new content yet when the handler returns.
 *
 * `keepFocus` is the second reveal of the same target: the caret is already
 * inside it and may have been moved out deliberately since, so re-focusing
 * would rip it away from wherever the reader went.
 */
export function useRevealTarget<T extends HTMLElement>() {
  const ref = useRef<T | null>(null)
  const frame = useRef<number | null>(null)

  useEffect(
    () => () => {
      if (frame.current !== null) cancelAnimationFrame(frame.current)
    },
    [],
  )

  const reveal = useCallback((options?: { keepFocus?: boolean }) => {
    const keepFocus = options?.keepFocus === true
    if (frame.current !== null) cancelAnimationFrame(frame.current)
    frame.current = requestAnimationFrame(() => {
      frame.current = null
      const el = ref.current
      if (!el) return
      if (!keepFocus) el.focus({ preventScroll: true })
      // Both calls are optional on purpose, not defensiveness for its own
      // sake: this runs inside a requestAnimationFrame callback, where a throw
      // is uncaught and takes the frame with it — and the focus above, when it
      // was asked for, already happened. jsdom implements neither.
      const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches === true
      el.scrollIntoView?.({ block: 'center', behavior: reduced ? 'auto' : 'smooth' })
    })
  }, [])

  return { ref, reveal }
}

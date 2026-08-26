import { useEffect, type RefObject } from 'react'

/**
 * Hands initial focus to a dialog's landing control.
 *
 * `scroll` is not cosmetic. The default suppresses scrolling because the URL
 * field is at the top of the dialog and a jump there is pure jitter; but the
 * image panel stacks BELOW the fold on narrow viewports (INV-165), so focusing
 * it without scrolling leaves the reader looking at the form they did not ask
 * for while the focused control sits off-screen.
 */
export function useDialogInitialFocus(
  open: boolean,
  dialogRef: RefObject<HTMLElement | null>,
  target: RefObject<HTMLElement | null>,
  scroll = false,
): void {
  useEffect(() => {
    if (!open) return
    const frame = requestAnimationFrame(() => {
      const active = document.activeElement
      const alreadyClaimed = active && active !== document.body && dialogRef.current?.contains(active)
      if (alreadyClaimed) return
      target.current?.focus({ preventScroll: !scroll })
      // Optional call, not defensiveness for its own sake: this runs inside a
      // requestAnimationFrame callback, where a throw is uncaught and takes
      // the frame with it. jsdom has no scrollIntoView at all, and focus —
      // the part that matters — has already happened by this line.
      if (scroll) target.current?.scrollIntoView?.({ block: 'center' })
    })
    return () => cancelAnimationFrame(frame)
  }, [dialogRef, target, open, scroll])
}

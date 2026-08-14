import { useEffect, type RefObject } from 'react'

export function useDialogInitialFocus(
  open: boolean,
  dialogRef: RefObject<HTMLElement | null>,
  inputRef: RefObject<HTMLInputElement | null>,
): void {
  useEffect(() => {
    if (!open) return
    const frame = requestAnimationFrame(() => {
      const active = document.activeElement
      const alreadyClaimed = active && active !== document.body && dialogRef.current?.contains(active)
      if (!alreadyClaimed) inputRef.current?.focus({ preventScroll: true })
    })
    return () => cancelAnimationFrame(frame)
  }, [dialogRef, inputRef, open])
}

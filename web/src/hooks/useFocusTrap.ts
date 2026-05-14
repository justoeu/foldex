import { useEffect, type RefObject } from 'react'

// Focusable selector covers the elements that screen readers + Tab traverse.
// Disabled, hidden, and tabindex=-1 nodes are excluded by the not-clauses.
const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'textarea:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

// useFocusTrap keeps Tab / Shift+Tab cycling inside the referenced container
// while `open` is true. Mounts a keydown listener on the container that
// re-routes Tab from the last element back to the first (and vice versa).
//
// On open: moves focus to the first focusable inside the container if focus
// is currently outside. On unmount/close: restores focus to whatever had it
// before the trap engaged.
export function useFocusTrap<T extends HTMLElement>(ref: RefObject<T | null>, open: boolean) {
  useEffect(() => {
    if (!open) return
    const node = ref.current
    if (!node) return

    const previouslyFocused = document.activeElement as HTMLElement | null

    const focusables = () =>
      Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => !el.hasAttribute('disabled') && el.offsetParent !== null,
      )

    // Pull focus in if it's currently outside the container (e.g. on body).
    if (!node.contains(document.activeElement)) {
      const first = focusables()[0]
      first?.focus()
    }

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return
      const list = focusables()
      if (list.length === 0) {
        e.preventDefault()
        return
      }
      const first = list[0]
      const last = list[list.length - 1]
      const active = document.activeElement as HTMLElement | null

      if (e.shiftKey) {
        if (active === first || !node.contains(active)) {
          e.preventDefault()
          last.focus()
        }
        return
      }
      if (active === last || !node.contains(active)) {
        e.preventDefault()
        first.focus()
      }
    }

    node.addEventListener('keydown', onKeyDown)
    return () => {
      node.removeEventListener('keydown', onKeyDown)
      // Restore focus to whatever opened the trap. Guard against the node
      // being detached (e.g. user closed via Esc + navigated).
      if (previouslyFocused && document.contains(previouslyFocused)) {
        previouslyFocused.focus()
      }
    }
  }, [ref, open])
}

import { useEffect } from 'react'

// Esc-handler STACK. Each call to useEscape pushes onto this stack on mount
// and pops on unmount. Only the topmost handler fires on Esc — so opening a
// modal "shadows" the folder-navigation Esc that the modal sits on top of.
// Without this, both fire and the user closes the modal AND pops the folder
// in one keypress.
const escapeStack: Array<() => void> = []

if (typeof window !== 'undefined') {
  window.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return
    if (escapeStack.length === 0) return
    e.preventDefault()
    escapeStack[escapeStack.length - 1]()
  })
}

// Registers an Esc handler. `enabled` lets the caller pause it without
// unmounting (e.g., a Home view's navigateBack that's only active when a
// folder is open). Useful for the call site to stay declarative.
export function useEscape(onEscape: () => void, enabled = true) {
  useEffect(() => {
    if (!enabled) return
    escapeStack.push(onEscape)
    return () => {
      const i = escapeStack.lastIndexOf(onEscape)
      if (i !== -1) escapeStack.splice(i, 1)
    }
  }, [onEscape, enabled])
}

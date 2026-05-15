import { useEffect } from 'react'
import { looksLikeUrl } from '../lib/url'

// Document-level Ctrl/Cmd+V handler that opens the New Link dialog with the
// clipboard URL pre-filled. Designed to be a "shortcut you don't have to
// learn" — the user pastes any URL anywhere on the page and the link form
// just appears. Gestures that would normally accept text (typing in a
// field, paste inside a modal, paste while a dialog is up) take priority
// and the paste flows through untouched.
//
// The hook is intentionally a no-op when:
//   - the paste target is editable (INPUT/TEXTAREA/SELECT/contentEditable)
//   - any modal/overlay is already mounted (.fx-overlay in the DOM)
//   - the clipboard payload isn't URL-shaped (see lib/url.ts)
//
// On a valid URL match it preventDefaults the event so the URL doesn't
// also paste into wherever the page may have focused next, then calls
// onUrl with the trimmed payload. The caller is expected to flip the
// dialog's open state and pass the URL down as `initialUrl`.
export function usePasteUrl(onUrl: (url: string) => void) {
  useEffect(() => {
    const isEditable = (el: EventTarget | null): boolean => {
      if (!(el instanceof HTMLElement)) return false
      const tag = el.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true
      if (el.isContentEditable) return true
      return false
    }
    const handler = (e: ClipboardEvent) => {
      if (isEditable(e.target)) return
      // Any open overlay (LinkDialog, FolderDialog, ConfirmDialog, palette,
      // backup dialog, …) means the user is mid-flow — don't hijack it.
      if (document.querySelector('.fx-overlay')) return
      const text = e.clipboardData?.getData('text/plain') ?? ''
      const trimmed = text.trim()
      if (!looksLikeUrl(trimmed)) return
      e.preventDefault()
      onUrl(trimmed)
    }
    document.addEventListener('paste', handler)
    return () => document.removeEventListener('paste', handler)
  }, [onUrl])
}

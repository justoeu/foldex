import { useEffect } from 'react'
import { entryAnchor } from '../lib/entryAnchor'

export type RevealTarget = { kind: 'link'; id: number }

/**
 * Scrolls a grid/list entry into view after navigation.
 *
 * The card is often not mounted when the click handler returns (folder
 * entries load after `jumpToFolder`), so this waits until `ready` and the
 * `[data-entry]` node exist. Clearing the target is the caller's job after
 * the highlight finishes — not the cleanup — so a Strict Mode remount does
 * not drop a reveal that has not happened yet.
 */
const PULSE_MS = 1400
const GIVE_UP_MS = 4000

export function useRevealEntry(
  target: RevealTarget | null,
  ready: boolean,
  onRevealed: () => void,
) {
  useEffect(() => {
    if (!target) return
    if (!ready) {
      const giveUp = window.setTimeout(onRevealed, GIVE_UP_MS)
      return () => window.clearTimeout(giveUp)
    }
    const el = document.querySelector(`[data-entry="${entryAnchor(target.kind, target.id)}"]`)
    if (!(el instanceof HTMLElement)) {
      const giveUp = window.setTimeout(onRevealed, GIVE_UP_MS)
      return () => window.clearTimeout(giveUp)
    }
    const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches === true
    el.focus?.({ preventScroll: true })
    el.scrollIntoView?.({ block: 'center', behavior: reduced ? 'auto' : 'smooth' })
    el.classList.add('fx-entry-reveal')
    const timer = window.setTimeout(() => {
      el.classList.remove('fx-entry-reveal')
      onRevealed()
    }, PULSE_MS)
    return () => {
      window.clearTimeout(timer)
      el.classList.remove('fx-entry-reveal')
    }
  }, [target, ready, onRevealed])
}

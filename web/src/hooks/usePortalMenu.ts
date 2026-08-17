import { useEffect, useRef, useState, useCallback } from 'react'

export type MenuPos = { top: number; right: number }

/**
 * Shared plumbing for the topbar's portaled dropdown menus (locale picker,
 * user menu). The topbar sets `overflow: hidden` — so its CTAs can't spill
 * out of the rounded card — which clips an absolutely-positioned dropdown;
 * the menu must live in <body> with fixed geometry captured from the anchor
 * button at open time.
 *
 * Returns the state plus BOTH refs: callers attach `btnRef` to their trigger
 * and `menuRef` to the portaled menu, so the outside-mousedown check can
 * whitelist clicks on either (closing on the option's own mousedown would
 * unmount it before the click fires).
 */
export function usePortalMenu() {
  const [open, setOpen] = useState(false)
  const [pos, setPos] = useState<MenuPos | null>(null)
  const btnRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node
      if (btnRef.current?.contains(target) || menuRef.current?.contains(target)) return
      setOpen(false)
    }
    const close = () => setOpen(false)
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('mousedown', onDown)
    // The fixed-positioned portal goes stale on scroll/resize — close rather
    // than track the anchor (a click away reopens it in the right place).
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('mousedown', onDown)
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('resize', close)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const toggle = useCallback(() => {
    // Capture geometry BEFORE flipping state, so the updater stays pure
    // (React StrictMode double-invokes updaters).
    if (btnRef.current) {
      const rect = btnRef.current.getBoundingClientRect()
      setPos({ top: rect.bottom + 8, right: window.innerWidth - rect.right })
    }
    setOpen((v) => !v)
  }, [])

  const close = useCallback(() => setOpen(false), [])

  return { open, pos, btnRef, menuRef, toggle, close, setOpen }
}

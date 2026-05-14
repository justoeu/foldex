import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

type Side = 'top' | 'bottom' | 'left' | 'right'

type Anchor = {
  text: string
  rect: DOMRect
  side: Side
} | null

// Global, viewport-aware tooltip. Mounted once at the app root. Listens for
// pointer/focus on any element with `data-tooltip="..."`; renders a single
// fixed-position chip via portal, then clamps its coords to the visible
// viewport so it never gets cut off near a screen edge (the exact bug that
// pure CSS `::after` tooltips can't solve without JavaScript).
//
// API matches the legacy CSS implementation:
//   data-tooltip="..."           — text to show
//   data-tooltip-side="top|..."  — preferred side, default `bottom`
// The pseudo-element rules in overrides.css were removed so we don't get a
// double-rendered chip.
export function TooltipPortal() {
  const [anchor, setAnchor] = useState<Anchor>(null)
  const showTimer = useRef<number | null>(null)
  const currentEl = useRef<Element | null>(null)

  useEffect(() => {
    const clearTimer = () => {
      if (showTimer.current !== null) {
        window.clearTimeout(showTimer.current)
        showTimer.current = null
      }
    }
    const open = (el: Element) => {
      if (currentEl.current === el) return
      const text = el.getAttribute('data-tooltip')
      if (!text) return
      currentEl.current = el
      const side = (el.getAttribute('data-tooltip-side') ?? 'bottom') as Side
      clearTimer()
      // Small delay to match the previous CSS hover transition (180ms).
      showTimer.current = window.setTimeout(() => {
        if (currentEl.current !== el) return
        setAnchor({ text, side, rect: el.getBoundingClientRect() })
      }, 180)
    }
    const close = () => {
      currentEl.current = null
      clearTimer()
      setAnchor(null)
    }

    const onOver = (e: MouseEvent) => {
      const t = (e.target as HTMLElement | null)?.closest('[data-tooltip]')
      if (t) open(t)
    }
    const onOut = (e: MouseEvent) => {
      const t = (e.target as HTMLElement | null)?.closest('[data-tooltip]')
      if (!t) return
      const next = e.relatedTarget as Node | null
      if (!next || !t.contains(next)) {
        if (currentEl.current === t) close()
      }
    }
    const onFocusIn = (e: FocusEvent) => {
      const t = (e.target as HTMLElement | null)?.closest('[data-tooltip]')
      if (t) open(t)
    }
    const onFocusOut = (e: FocusEvent) => {
      const t = (e.target as HTMLElement | null)?.closest('[data-tooltip]')
      if (!t) return
      if (currentEl.current === t) close()
    }
    const onScroll = () => close()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close()
    }

    document.addEventListener('mouseover', onOver)
    document.addEventListener('mouseout', onOut)
    document.addEventListener('focusin', onFocusIn)
    document.addEventListener('focusout', onFocusOut)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onScroll)
    window.addEventListener('keydown', onKey)
    return () => {
      clearTimer()
      document.removeEventListener('mouseover', onOver)
      document.removeEventListener('mouseout', onOut)
      document.removeEventListener('focusin', onFocusIn)
      document.removeEventListener('focusout', onFocusOut)
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onScroll)
      window.removeEventListener('keydown', onKey)
    }
  }, [])

  if (!anchor) return null
  return createPortal(
    <TooltipChip text={anchor.text} rect={anchor.rect} side={anchor.side} />,
    document.body,
  )
}

function TooltipChip({ text, rect, side }: { text: string; rect: DOMRect; side: Side }) {
  const ref = useRef<HTMLDivElement>(null)
  // Render at (0,0) hidden first so we can measure, then place + reveal in
  // the same paint to avoid a visible jump.
  const [pos, setPos] = useState<{ left: number; top: number; ready: boolean }>({
    left: 0,
    top: 0,
    ready: false,
  })

  useLayoutEffect(() => {
    if (!ref.current) return
    const tip = ref.current.getBoundingClientRect()
    const m = 8 // gap between anchor and tip
    const vpW = window.innerWidth
    const vpH = window.innerHeight

    let left = 0
    let top = 0
    switch (side) {
      case 'top':
        left = rect.left + rect.width / 2 - tip.width / 2
        top = rect.top - tip.height - m
        break
      case 'bottom':
        left = rect.left + rect.width / 2 - tip.width / 2
        top = rect.bottom + m
        break
      case 'left':
        left = rect.left - tip.width - m
        top = rect.top + rect.height / 2 - tip.height / 2
        break
      case 'right':
        left = rect.right + m
        top = rect.top + rect.height / 2 - tip.height / 2
        break
    }

    // Clamp to visible viewport (leave a 6px breathing margin).
    const edge = 6
    left = Math.max(edge, Math.min(left, vpW - tip.width - edge))
    top = Math.max(edge, Math.min(top, vpH - tip.height - edge))
    setPos({ left, top, ready: true })
  }, [text, side, rect.top, rect.left, rect.right, rect.bottom])

  return (
    <div
      ref={ref}
      className="fx-tip"
      role="tooltip"
      style={{
        position: 'fixed',
        left: pos.left,
        top: pos.top,
        opacity: pos.ready ? 1 : 0,
      }}
    >
      {text}
    </div>
  )
}

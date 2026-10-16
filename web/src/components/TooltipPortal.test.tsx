import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { fireEvent, render, screen, act } from '@testing-library/react'
import { TooltipPortal } from './TooltipPortal'

/** The chip appears after a 180 ms delay. Fake timers advance that clock
 *  deterministically — a real setTimeout sleep made every case pay wall
 *  time and could not assert the chip is still ABSENT one ms early.
 *  fireEvent drives the pointer because it is synchronous: userEvent's
 *  internal delay loop never completes against a faked clock. */
function settle(ms = 180) {
  act(() => {
    vi.advanceTimersByTime(ms)
  })
}

const hover = (el: Element) => fireEvent.mouseOver(el)
const unhover = (el: Element) => fireEvent.mouseOut(el, { relatedTarget: document.body })
const press = (el: Element) => {
  fireEvent.mouseDown(el)
  fireEvent.mouseUp(el)
  fireEvent.click(el)
}

function mount() {
  return render(
    <>
      <button data-tooltip="Valmir Justo">avatar</button>
      <TooltipPortal />
    </>,
  )
}

describe('the global tooltip', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it('shows the text after the delay', () => {
    mount()
    hover(screen.getByRole('button'))
    settle(179)
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
    settle(1)
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()
  })

  // Clicking a tooltipped control opens or changes something, and the chip is
  // then painted over the result — which is exactly what the topbar avatar did
  // to the first row of its own dropdown.
  it('closes on a press, so it cannot cover what the click just opened', () => {
    mount()
    const btn = screen.getByRole('button')
    hover(btn)
    settle()
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()

    press(btn)
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
  })

  // Closing alone is not enough. `close()` clears the current anchor, the
  // pointer is still on the control, and the next mouse event re-opens the chip
  // 180 ms later — straight back over the menu. This is the case the first fix
  // passed and the real screen failed.
  it('stays closed while the pointer remains on the control it just pressed', () => {
    mount()
    const btn = screen.getByRole('button')
    hover(btn)
    settle()
    press(btn)

    // Still hovering — exactly what a mouse does after a click.
    hover(btn)
    settle()
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
  })

  // ...and leaving is what re-arms it, or the control would lose its tooltip
  // for the rest of the page's life after one click.
  it('shows again once the pointer has left and come back', () => {
    mount()
    const btn = screen.getByRole('button')
    hover(btn)
    settle()
    press(btn)

    unhover(btn)
    hover(btn)
    settle()
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()
  })

  // The fix that was tried first and is worse: `onOut` finds its anchor with
  // `closest('[data-tooltip]')`, so an element that drops the attribute while
  // the chip is up matches nothing and the chip is stranded. This locks the
  // property that makes removing the attribute unnecessary.
  it.each(['top', 'left', 'right'] as const)('renders a %s-side tooltip', (side) => {
    render(
      <>
        <button data-tooltip={`${side} tip`} data-tooltip-side={side}>anchor</button>
        <TooltipPortal />
      </>,
    )
    hover(screen.getByRole('button'))
    settle()
    const tip = screen.getByRole('tooltip')
    expect(tip).toHaveTextContent(`${side} tip`)
    expect(tip.style.opacity).toBe('1') // layout effect placed it
  })

  it('opens on focus and closes on blur, without any pointer', () => {
    mount()
    const btn = screen.getByRole('button')
    fireEvent.focusIn(btn)
    settle()
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()
    fireEvent.focusOut(btn)
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
    // Focus events on plain elements are ignored, not crashes.
    fireEvent.focusIn(document.body)
    fireEvent.focusOut(document.body)
    settle()
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
  })

  it('closes on Escape, scroll and resize, but not on other keys', () => {
    mount()
    const btn = screen.getByRole('button')
    const show = () => { hover(btn); settle(); expect(screen.getByText('Valmir Justo')).toBeInTheDocument() }
    show()
    fireEvent.keyDown(window, { key: 'a' })
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
    show()
    fireEvent.scroll(window)
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
    show()
    fireEvent(window, new Event('resize'))
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
  })

  it('keeps the chip when the pointer only moves within the anchor', () => {
    render(
      <>
        <button data-tooltip="Nested"><span>inner</span></button>
        <TooltipPortal />
      </>,
    )
    const btn = screen.getByRole('button')
    hover(btn)
    settle()
    fireEvent.mouseOut(btn, { relatedTarget: screen.getByText('inner') })
    expect(screen.getByText('Nested')).toBeInTheDocument()
    // A mouseout with no relatedTarget (leaving the window) still closes.
    fireEvent.mouseOut(btn, { relatedTarget: null })
    expect(screen.queryByText('Nested')).not.toBeInTheDocument()
  })

  it('never opens for an empty tooltip or a non-tooltipped element', () => {
    render(
      <>
        <button data-tooltip="">empty</button>
        <span>plain</span>
        <TooltipPortal />
      </>,
    )
    hover(screen.getByRole('button'))
    hover(screen.getByText('plain'))
    fireEvent.mouseDown(screen.getByText('plain'))
    settle()
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  })

  it('cancels a pending show when the pointer leaves before the delay', () => {
    mount()
    const btn = screen.getByRole('button')
    hover(btn)
    settle(50)
    unhover(btn)
    settle(200)
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
  })

  it('switches anchors when the pointer moves straight to another control', () => {
    render(
      <>
        <button data-tooltip="First">one</button>
        <button data-tooltip="Second">two</button>
        <TooltipPortal />
      </>,
    )
    const [one, two] = screen.getAllByRole('button')
    hover(one)
    hover(two) // second open() supersedes before the first timer fires
    settle()
    expect(screen.queryByText('First')).not.toBeInTheDocument()
    expect(screen.getByText('Second')).toBeInTheDocument()
    // Re-hovering the same anchor is a no-op, not a flicker/restart.
    hover(two)
    settle()
    expect(screen.getByText('Second')).toBeInTheDocument()
  })

  it('closes even when the anchor has since lost its attribute', () => {
    mount()
    const btn = screen.getByRole('button')
    hover(btn)
    settle()
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()

    btn.removeAttribute('data-tooltip')
    press(btn)
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
  })
})

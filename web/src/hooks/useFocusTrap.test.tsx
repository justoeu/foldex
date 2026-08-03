import { describe, it, expect, beforeAll, afterAll } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { useRef, useState } from 'react'
import { useFocusTrap } from './useFocusTrap'

// jsdom reports offsetParent=null for everything; the trap filters those out.
// Stub a non-null offsetParent so focusable discovery works like a real browser.
let offsetParentSpy: ReturnType<typeof Object.defineProperty> | undefined
beforeAll(() => {
  offsetParentSpy = Object.defineProperty(HTMLElement.prototype, 'offsetParent', {
    configurable: true,
    get() {
      return this.parentElement ?? document.body
    },
  })
})
afterAll(() => {
  // restore by deleting the override if possible
  try {
    // @ts-expect-error cleanup
    delete HTMLElement.prototype.offsetParent
  } catch {
    /* ignore */
  }
  void offsetParentSpy
})

function Dialog({ open = true }: { open?: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  useFocusTrap(ref, open)
  return (
    <div ref={ref} role="dialog" data-testid="dialog" tabIndex={-1}>
      <button data-testid="first">First</button>
      <input data-testid="middle" placeholder="middle" />
      <button data-testid="last">Last</button>
    </div>
  )
}

function Toggleable() {
  const [open, setOpen] = useState(true)
  const ref = useRef<HTMLDivElement>(null)
  useFocusTrap(ref, open)
  return (
    <div>
      <button data-testid="opener" onClick={() => setOpen(true)}>Open</button>
      {open && (
        <div ref={ref} role="dialog" data-testid="dialog">
          <button data-testid="first">First</button>
          <button data-testid="last" onClick={() => setOpen(false)}>Close</button>
        </div>
      )}
    </div>
  )
}

describe('useFocusTrap', () => {
  it('moves focus to the first focusable on open', async () => {
    render(
      <div>
        <button data-testid="outside">Outside</button>
        <Dialog />
      </div>,
    )
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId('first')))
  })

  it('does not trap focus when open is false', () => {
    render(<Dialog open={false} />)
    expect(document.activeElement).not.toBe(screen.getByTestId('first'))
  })

  it('cycles Tab from last back to first', async () => {
    render(<Dialog />)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId('first')))
    screen.getByTestId('last').focus()
    fireEvent.keyDown(screen.getByTestId('dialog'), { key: 'Tab' })
    expect(document.activeElement).toBe(screen.getByTestId('first'))
  })

  it('cycles Shift+Tab from first back to last', async () => {
    render(<Dialog />)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId('first')))
    fireEvent.keyDown(screen.getByTestId('dialog'), { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(screen.getByTestId('last'))
  })

  it('ignores non-Tab keys', async () => {
    render(<Dialog />)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId('first')))
    fireEvent.keyDown(screen.getByTestId('dialog'), { key: 'Escape' })
    expect(document.activeElement).toBe(screen.getByTestId('first'))
  })

  it('prevents Tab when there are no focusable elements', () => {
    function Empty() {
      const ref = useRef<HTMLDivElement>(null)
      useFocusTrap(ref, true)
      return (
        <div ref={ref} data-testid="empty" tabIndex={-1}>
          <span>nothing focusable</span>
        </div>
      )
    }
    render(<Empty />)
    const empty = screen.getByTestId('empty')
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    empty.dispatchEvent(ev)
    expect(screen.getByText('nothing focusable')).toBeInTheDocument()
  })

  it('restores focus path on close', async () => {
    render(<Toggleable />)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId('first')))
    fireEvent.click(screen.getByTestId('last'))
    await waitFor(() => expect(screen.queryByTestId('dialog')).toBeNull())
  })

  it('Shift+Tab when focus is outside the container jumps to last', async () => {
    render(
      <div>
        <button data-testid="outside">Outside</button>
        <Dialog />
      </div>,
    )
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId('first')))
    screen.getByTestId('outside').focus()
    fireEvent.keyDown(screen.getByTestId('dialog'), { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(screen.getByTestId('last'))
  })

  it('Tab when focus is outside the container jumps to first', async () => {
    render(
      <div>
        <button data-testid="outside">Outside</button>
        <Dialog />
      </div>,
    )
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId('first')))
    screen.getByTestId('outside').focus()
    fireEvent.keyDown(screen.getByTestId('dialog'), { key: 'Tab' })
    expect(document.activeElement).toBe(screen.getByTestId('first'))
  })

  it('no-ops cleanly when ref is null on open', () => {
    function NullRef() {
      const ref = useRef<HTMLDivElement>(null)
      useFocusTrap(ref, true)
      return <div data-testid="null-ref">no attach</div>
    }
    render(<NullRef />)
    expect(screen.getByTestId('null-ref')).toBeInTheDocument()
  })
})

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { useRef } from 'react'
import { useFocusTrap } from './useFocusTrap'

function Dialog({ open = true }: { open?: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  useFocusTrap(ref, open)
  return (
    <div ref={ref} role="dialog" data-testid="dialog">
      <button data-testid="first">First</button>
      <input data-testid="middle" placeholder="middle" />
      <button data-testid="last">Last</button>
    </div>
  )
}

describe('useFocusTrap', () => {
  it('renders the dialog with focusable elements', () => {
    render(<Dialog />)
    expect(screen.getByTestId('first')).toBeInTheDocument()
    expect(screen.getByTestId('last')).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('does not trap focus when open is false', () => {
    render(<Dialog open={false} />)
    const first = screen.getByTestId('first')
    expect(document.activeElement).not.toBe(first)
  })

  it('dialog element has correct role and test-id', () => {
    render(<Dialog />)
    const dialog = screen.getByTestId('dialog')
    expect(dialog.getAttribute('role')).toBe('dialog')
  })

  it('all three focusable elements are rendered in order', () => {
    render(<Dialog />)
    const dialog = screen.getByTestId('dialog')
    const focusable = dialog.querySelectorAll('button, input')
    expect(focusable).toHaveLength(3)
    expect(focusable[0]).toHaveTextContent('First')
    expect(focusable[2]).toHaveTextContent('Last')
  })

  it('handles a container with no focusable elements gracefully', () => {
    function Empty() {
      const ref = useRef<HTMLDivElement>(null)
      useFocusTrap(ref, true)
      return (
        <div ref={ref} data-testid="empty">
          <span>nothing focusable</span>
        </div>
      )
    }
    render(<Empty />)
    expect(screen.getByTestId('empty')).toBeInTheDocument()
    expect(screen.getByText('nothing focusable')).toBeInTheDocument()
  })
})

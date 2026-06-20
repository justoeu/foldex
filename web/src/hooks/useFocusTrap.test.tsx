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
    expect(screen.getByTestId('dialog')).toBeInTheDocument()
  })

  it('does not trap focus when open is false', () => {
    render(<Dialog open={false} />)
    const first = screen.getByTestId('first')
    expect(document.activeElement).not.toBe(first)
  })

  it('dialog element is present and has the dialog role', () => {
    render(<Dialog />)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

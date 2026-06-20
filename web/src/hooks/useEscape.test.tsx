import { describe, it, expect, vi } from 'vitest'
import { render, fireEvent } from '@testing-library/react'
import { useEscape } from './useEscape'

function Tester({ onEsc, enabled = true }: { onEsc: () => void; enabled?: boolean }) {
  useEscape(onEsc, enabled)
  return <div data-testid="target" />
}

describe('useEscape', () => {
  it('fires the handler on Escape keydown', () => {
    const onEsc = vi.fn()
    render(<Tester onEsc={onEsc} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onEsc).toHaveBeenCalledOnce()
  })

  it('does not fire for non-Escape keys', () => {
    const onEsc = vi.fn()
    render(<Tester onEsc={onEsc} />)
    fireEvent.keyDown(document, { key: 'Enter' })
    expect(onEsc).not.toHaveBeenCalled()
  })

  it('does not fire when enabled is false', () => {
    const onEsc = vi.fn()
    render(<Tester onEsc={onEsc} enabled={false} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onEsc).not.toHaveBeenCalled()
  })

  it('unregisters on unmount', () => {
    const onEsc = vi.fn()
    const { unmount } = render(<Tester onEsc={onEsc} />)
    unmount()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onEsc).not.toHaveBeenCalled()
  })

  it('calls the handler when enabled state changes to true', () => {
    const onEsc = vi.fn()
    const { rerender } = render(<Tester onEsc={onEsc} enabled={false} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onEsc).not.toHaveBeenCalled()
    rerender(<Tester onEsc={onEsc} enabled={true} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onEsc).toHaveBeenCalledOnce()
  })
})

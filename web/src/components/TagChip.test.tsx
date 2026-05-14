import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TagChip } from './TagChip'

const solidTag = { name: 'jira', color: '#6366F1' }
const gradientTag = {
  name: 'rainbow',
  color: 'linear-gradient(135deg, #6366F1, #EC4899)',
}

describe('TagChip', () => {
  it('renders the tag name and a static span when not interactive', () => {
    render(<TagChip tag={solidTag} />)
    const node = screen.getByText('jira').closest('span')
    expect(node).toBeInTheDocument()
    expect(node?.tagName).toBe('SPAN')
  })

  it('renders as a button when onClick is provided', async () => {
    const onClick = vi.fn()
    render(<TagChip tag={solidTag} onClick={onClick} />)
    const btn = screen.getByRole('button')
    expect(btn).toHaveTextContent('jira')
    await userEvent.setup().click(btn)
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('drives --chip-c with the primary (solid) color, even for gradients', () => {
    const { container } = render(<TagChip tag={gradientTag} />)
    const chip = container.querySelector('.fx-chip') as HTMLElement
    expect(chip.style.getPropertyValue('--chip-c')).toBe('#6366F1')
  })

  it('paints the dot with the full gradient when the tag uses one', () => {
    const { container } = render(<TagChip tag={gradientTag} />)
    const dot = container.querySelector('.fx-chip-dot') as HTMLElement
    expect(dot.getAttribute('style')).toContain('linear-gradient')
  })

  it('leaves the dot unstyled (inherits --chip-c) for solid tags', () => {
    const { container } = render(<TagChip tag={solidTag} />)
    const dot = container.querySelector('.fx-chip-dot') as HTMLElement
    expect(dot.getAttribute('style')).toBeNull()
  })

  it('fires onClose when the close button is clicked, without bubbling to onClick', async () => {
    const onClick = vi.fn()
    const onClose = vi.fn()
    render(<TagChip tag={solidTag} onClick={onClick} closable onClose={onClose} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /remove jira/i }))
    expect(onClose).toHaveBeenCalledOnce()
    expect(onClick).not.toHaveBeenCalled()
  })
})

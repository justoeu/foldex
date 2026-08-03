import { describe, it, expect, vi } from 'vitest'
import { screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { GradientPicker } from './GradientPicker'
import { renderWithProviders } from '../test/renderWithProviders'

describe('GradientPicker', () => {
  it('swaps colors via the swap button', async () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    await userEvent.setup().click(screen.getByLabelText(/swap gradient/i))
    expect(onChange).toHaveBeenCalledWith('#EC4899', '#6366F1')
  })

  it('picks a preset color for the start stop', async () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    await userEvent.setup().click(screen.getByLabelText(/Start #0EA5E9/i))
    expect(onChange).toHaveBeenCalledWith('#0EA5E9', '#EC4899')
  })

  it('picks a preset color for the end stop', async () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    await userEvent.setup().click(screen.getByLabelText(/End #10B981/i))
    expect(onChange).toHaveBeenCalledWith('#6366F1', '#10B981')
  })

  it('accepts custom color input on a stop', async () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    const customs = screen.getAllByLabelText(/custom/i)
    fireEvent.change(customs[0], { target: { value: '#abcdef' } })
    expect(onChange).toHaveBeenCalledWith('#abcdef', '#EC4899')
  })

  it('swaps when dragging one stop onto the other', () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    const stops = document.querySelectorAll('.fx-gradient-stop')
    const heads = document.querySelectorAll('.fx-gradient-stop-head')
    const dt = {
      types: ['application/x-foldex-gradient-stop'],
      setData: vi.fn(),
      getData: vi.fn(() => 'from'),
      effectAllowed: 'all',
      dropEffect: 'none',
    }
    fireEvent.dragStart(heads[0], { dataTransfer: dt })
    fireEvent.dragEnter(stops[1], { dataTransfer: dt })
    fireEvent.dragOver(stops[1], { dataTransfer: dt })
    fireEvent.drop(stops[1], { dataTransfer: dt })
    expect(onChange).toHaveBeenCalledWith('#EC4899', '#6366F1')
    fireEvent.dragEnd(heads[0])
  })

  it('ignores drop of the same side', () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    const stops = document.querySelectorAll('.fx-gradient-stop')
    const dt = {
      types: ['application/x-foldex-gradient-stop'],
      getData: vi.fn(() => 'from'),
      dropEffect: 'none',
    }
    fireEvent.drop(stops[0], { dataTransfer: dt })
    expect(onChange).not.toHaveBeenCalled()
  })

  it('drags hue thumbs and bar clicks update colors', () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    const bar = screen.getByRole('slider', { name: /hue/i })
    // Mock getBoundingClientRect for position math.
    vi.spyOn(bar, 'getBoundingClientRect').mockReturnValue({
      left: 0, top: 0, width: 200, height: 12, right: 200, bottom: 12, x: 0, y: 0, toJSON: () => ({}),
    } as DOMRect)
    bar.setPointerCapture = vi.fn()

    fireEvent.pointerDown(bar, { clientX: 100, pointerId: 1 })
    expect(onChange).toHaveBeenCalled()
    onChange.mockClear()

    fireEvent.pointerMove(bar, { clientX: 150, pointerId: 1 })
    // may or may not fire depending on dragging state set by previous down
    fireEvent.pointerUp(bar)

    const thumbs = document.querySelectorAll('.fx-hue-thumb')
    fireEvent.pointerDown(thumbs[0], { clientX: 20, pointerId: 2 })
    expect(onChange).toHaveBeenCalled()
    fireEvent.pointerDown(thumbs[1], { clientX: 180, pointerId: 3 })
  })

  it('handles near-neutral colors with S/L floor on hue drag', () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#111111" to="#eeeeee" onChange={onChange} />)
    const bar = screen.getByRole('slider', { name: /hue/i })
    vi.spyOn(bar, 'getBoundingClientRect').mockReturnValue({
      left: 0, top: 0, width: 100, height: 12, right: 100, bottom: 12, x: 0, y: 0, toJSON: () => ({}),
    } as DOMRect)
    bar.setPointerCapture = vi.fn()
    fireEvent.pointerDown(bar, { clientX: 50, pointerId: 1 })
    expect(onChange).toHaveBeenCalled()
    const [from] = onChange.mock.calls[0]
    expect(from).toMatch(/^#/)
  })
})

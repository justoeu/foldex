import { describe, it, expect, vi } from 'vitest'
import { screen, fireEvent, createEvent } from '@testing-library/react'
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
    const bar = screen.getByRole('group', { name: /hue/i })
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

  it('exposes each hue thumb as a slider with a numeric value', () => {
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={vi.fn()} />)
    const thumbs = screen.getAllByRole('slider')
    expect(thumbs).toHaveLength(2)
    expect(thumbs[0]).toHaveAttribute('aria-valuenow')
    expect(thumbs[1]).toHaveAttribute('aria-valuemin', '0')
    expect(thumbs[1]).toHaveAttribute('aria-valuemax', '360')
  })

  it('moves a hue thumb with arrow keys', async () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    const start = screen.getByRole('slider', { name: /^Start$/i })
    start.focus()
    await userEvent.setup().keyboard('{ArrowRight}')
    expect(onChange).toHaveBeenCalled()
    const [from] = onChange.mock.calls[0]
    expect(from).toMatch(/^#/)
    expect(from).not.toBe('#6366F1')
  })

  it('clears the drag-over highlight only when the pointer really leaves the stop', () => {
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={vi.fn()} />)
    const stops = document.querySelectorAll('.fx-gradient-stop')
    const dt = { types: ['application/x-foldex-gradient-stop'] }
    // fireEvent's init dict drops relatedTarget for drag events; define it.
    const dragLeaveTo = (node: Element, related: EventTarget | null) => {
      const ev = createEvent.dragLeave(node, { dataTransfer: dt })
      Object.defineProperty(ev, 'relatedTarget', { value: related })
      fireEvent(node, ev)
    }
    fireEvent.dragEnter(stops[1], { dataTransfer: dt })
    expect(stops[1].className).toContain('fx-gradient-stop-over')
    // Moving onto a child of the same stop is not a leave.
    dragLeaveTo(stops[1], stops[1].firstChild)
    expect(stops[1].className).toContain('fx-gradient-stop-over')
    dragLeaveTo(stops[1], document.body)
    expect(stops[1].className).not.toContain('fx-gradient-stop-over')
    // A leave with no relatedTarget (left the window) also clears it.
    fireEvent.dragEnter(stops[1], { dataTransfer: dt })
    dragLeaveTo(stops[1], null)
    expect(stops[1].className).not.toContain('fx-gradient-stop-over')
  })

  it('ignores unrelated drags over a stop', () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    const stops = document.querySelectorAll('.fx-gradient-stop')
    const dt = { types: ['text/plain'], getData: vi.fn(() => ''), dropEffect: 'none' }
    fireEvent.dragEnter(stops[0], { dataTransfer: dt })
    expect(stops[0].className).not.toContain('fx-gradient-stop-over')
    fireEvent.dragOver(stops[0], { dataTransfer: dt })
    fireEvent.drop(stops[0], { dataTransfer: dt })
    expect(onChange).not.toHaveBeenCalled()
  })

  it('jumps hue thumbs with Home/End and steps by 10 with Shift', async () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    const user = userEvent.setup()
    const end = screen.getByRole('slider', { name: /^End$/i })
    end.focus()
    await user.keyboard('{End}')
    expect(onChange).toHaveBeenCalledWith('#6366F1', expect.stringMatching(/^#/))
    onChange.mockClear()
    await user.keyboard('{Home}')
    expect(onChange).toHaveBeenCalledWith('#6366F1', expect.stringMatching(/^#/))
    // Props are static in this render, so every key steps from the same hue:
    // Shift must move 10°, a plain arrow 1° — different resulting hexes.
    onChange.mockClear()
    await user.keyboard('{ArrowDown}')
    const step1 = onChange.mock.calls.at(-1)![1]
    onChange.mockClear()
    await user.keyboard('{Shift>}{ArrowDown}{/Shift}')
    expect(onChange.mock.calls.at(-1)![1]).not.toBe(step1)
    onChange.mockClear()
    await user.keyboard('{ArrowUp}')
    expect(onChange).toHaveBeenCalled()
    // Unrelated keys do not move the thumb.
    onChange.mockClear()
    await user.keyboard('x')
    expect(onChange).not.toHaveBeenCalled()
  })

  it('does not move a thumb on pointer move without an active drag, and cancels cleanly', () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    const bar = screen.getByRole('group', { name: /hue/i })
    vi.spyOn(bar, 'getBoundingClientRect').mockReturnValue({
      left: 0, top: 0, width: 360, height: 12, right: 360, bottom: 12, x: 0, y: 0, toJSON: () => ({}),
    } as DOMRect)
    bar.setPointerCapture = vi.fn()
    fireEvent.pointerMove(bar, { clientX: 90, pointerId: 1 })
    expect(onChange).not.toHaveBeenCalled()
    fireEvent.pointerDown(bar, { clientX: 90, pointerId: 1 })
    expect(bar.className).toContain('fx-hue-bar-dragging')
    onChange.mockClear()
    fireEvent.pointerMove(bar, { clientX: 180, pointerId: 1 })
    expect(onChange).toHaveBeenCalled()
    fireEvent.pointerCancel(bar)
    expect(bar.className).not.toContain('fx-hue-bar-dragging')
    onChange.mockClear()
    fireEvent.pointerMove(bar, { clientX: 270, pointerId: 1 })
    expect(onChange).not.toHaveBeenCalled()
  })

  it('snaps the NEAREST thumb when the bar itself is pressed', () => {
    const onChange = vi.fn()
    // from ≈ hue 239 (indigo), to ≈ hue 330 (pink): a press at hue ~36 is
    // nearer to neither edge — distance decides, here the start thumb.
    renderWithProviders(<GradientPicker from="#6366F1" to="#EC4899" onChange={onChange} />)
    const bar = screen.getByRole('group', { name: /hue/i })
    vi.spyOn(bar, 'getBoundingClientRect').mockReturnValue({
      left: 0, top: 0, width: 360, height: 12, right: 360, bottom: 12, x: 0, y: 0, toJSON: () => ({}),
    } as DOMRect)
    bar.setPointerCapture = vi.fn()
    fireEvent.pointerDown(bar, { clientX: 350, pointerId: 1 }) // hue 350 → nearest is pink (330)
    expect(onChange).toHaveBeenCalledWith('#6366F1', expect.stringMatching(/^#/))
    onChange.mockClear()
    fireEvent.pointerDown(bar, { clientX: 10, pointerId: 1 }) // hue 10 → still nearer pink? 330 dist 320 vs 239 dist 229 → from
    expect(onChange.mock.calls.at(-1)![1]).toBe('#EC4899')
  })

  it('handles near-neutral colors with S/L floor on hue drag', () => {
    const onChange = vi.fn()
    renderWithProviders(<GradientPicker from="#111111" to="#eeeeee" onChange={onChange} />)
    const bar = screen.getByRole('group', { name: /hue/i })
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

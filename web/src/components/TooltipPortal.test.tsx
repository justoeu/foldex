import { describe, it, expect } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TooltipPortal } from './TooltipPortal'

/** The chip appears after a 180 ms delay, so every case has to wait it out. */
async function settle() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 220))
  })
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
  it('shows the text after the delay', async () => {
    mount()
    await userEvent.hover(screen.getByRole('button'))
    await settle()
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()
  })

  // Clicking a tooltipped control opens or changes something, and the chip is
  // then painted over the result — which is exactly what the topbar avatar did
  // to the first row of its own dropdown.
  it('closes on a press, so it cannot cover what the click just opened', async () => {
    mount()
    const btn = screen.getByRole('button')
    await userEvent.hover(btn)
    await settle()
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()

    await userEvent.click(btn)
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
  })

  // Closing alone is not enough. `close()` clears the current anchor, the
  // pointer is still on the control, and the next mouse event re-opens the chip
  // 180 ms later — straight back over the menu. This is the case the first fix
  // passed and the real screen failed.
  it('stays closed while the pointer remains on the control it just pressed', async () => {
    mount()
    const btn = screen.getByRole('button')
    await userEvent.hover(btn)
    await settle()
    await userEvent.click(btn)

    // Still hovering — exactly what a mouse does after a click.
    await userEvent.hover(btn)
    await settle()
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
  })

  // ...and leaving is what re-arms it, or the control would lose its tooltip
  // for the rest of the page's life after one click.
  it('shows again once the pointer has left and come back', async () => {
    mount()
    const btn = screen.getByRole('button')
    await userEvent.hover(btn)
    await settle()
    await userEvent.click(btn)

    await userEvent.unhover(btn)
    await userEvent.hover(btn)
    await settle()
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()
  })

  // The fix that was tried first and is worse: `onOut` finds its anchor with
  // `closest('[data-tooltip]')`, so an element that drops the attribute while
  // the chip is up matches nothing and the chip is stranded. This locks the
  // property that makes removing the attribute unnecessary.
  it('closes even when the anchor has since lost its attribute', async () => {
    mount()
    const btn = screen.getByRole('button')
    await userEvent.hover(btn)
    await settle()
    expect(screen.getByText('Valmir Justo')).toBeInTheDocument()

    btn.removeAttribute('data-tooltip')
    await userEvent.click(btn)
    expect(screen.queryByText('Valmir Justo')).not.toBeInTheDocument()
  })
})

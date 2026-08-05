import { describe, it, expect, vi } from 'vitest'
import { useState } from 'react'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { OtpInput, OTP_LENGTH } from './OtpInput'
import { renderWithProviders } from '../../test/renderWithProviders'

/**
 * The component is controlled, so every test drives it through a tiny host that
 * owns the value — testing it with a frozen `value` prop would exercise nothing
 * but the initial render.
 */
function Host({ onComplete }: { onComplete?: (c: string) => void }) {
  const [value, setValue] = useState('')
  return (
    <>
      <OtpInput value={value} onChange={setValue} onComplete={onComplete} autoFocus />
      <output data-testid="value">{value}</output>
    </>
  )
}

function cells() {
  return screen.getAllByRole('textbox') as HTMLInputElement[]
}

describe('OtpInput', () => {
  it('renders one labelled cell per digit', () => {
    renderWithProviders(<Host />)
    const inputs = cells()
    expect(inputs).toHaveLength(OTP_LENGTH)
    // Every cell needs its own accessible name; six inputs called "code" leave
    // a screen-reader user with no way to know where they are.
    inputs.forEach((el, i) => {
      expect(el).toHaveAccessibleName(`Digit ${i + 1} of ${OTP_LENGTH}`)
    })
  })

  // Safari autofills EVERY input carrying autocomplete="one-time-code" with the
  // same digit when it reads a code from Messages, producing "111111". Only the
  // first cell may advertise it.
  it('advertises one-time-code on the first cell only', () => {
    renderWithProviders(<Host />)
    const inputs = cells()
    expect(inputs[0]).toHaveAttribute('autocomplete', 'one-time-code')
    inputs.slice(1).forEach((el) => {
      expect(el).toHaveAttribute('autocomplete', 'off')
    })
  })

  // `type="number"` would show spinners, accept "e" and "-", and drop leading
  // zeros — fatal for a code where "012345" is valid.
  it('uses a numeric text input rather than a number input', () => {
    renderWithProviders(<Host />)
    cells().forEach((el) => {
      expect(el).toHaveAttribute('type', 'text')
      expect(el).toHaveAttribute('inputmode', 'numeric')
      expect(el).toHaveAttribute('maxlength', '1')
    })
  })

  it('advances focus as digits are typed', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Host />)
    const inputs = cells()

    await user.type(inputs[0], '1')
    expect(inputs[1]).toHaveFocus()
    await user.type(inputs[1], '2')
    expect(inputs[2]).toHaveFocus()

    expect(screen.getByTestId('value')).toHaveTextContent('12')
  })

  it('ignores non-digits', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Host />)
    await user.type(cells()[0], 'a')
    expect(screen.getByTestId('value')).toBeEmptyDOMElement()
  })

  // Backspace on an EMPTY cell steps back and deletes there. (The filled-cell
  // case is the truncation test below.) Doing both in one press would make
  // correcting a single wrong digit impossible — the user would always destroy
  // the digit before it too.
  it('steps back and deletes when the current cell is already empty', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Host />)
    const inputs = cells()

    await user.type(inputs[0], '1')
    await user.type(inputs[1], '2')
    expect(screen.getByTestId('value')).toHaveTextContent('12')

    // Focus is on cell 2 (empty). Backspace there steps back to cell 1.
    await user.keyboard('{Backspace}')
    expect(screen.getByTestId('value')).toHaveTextContent('1')
    expect(inputs[1]).toHaveFocus()

    // Cell 1 holds "2"... which was just cleared, so this steps back again.
    await user.keyboard('{Backspace}')
    expect(screen.getByTestId('value')).toBeEmptyDOMElement()
    expect(inputs[0]).toHaveFocus()
  })

  // The case that exposed a real defect: `value` is a compact string, so
  // blanking a middle character and rejoining SHIFTS every later digit one cell
  // to the left — "123456" silently became "12456" with 4 sitting where 3 was.
  // Truncating from the edited cell is the honest resolution.
  it('truncates from the edited cell instead of shifting later digits', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Host />)
    const inputs = cells()

    inputs[0].focus()
    await user.paste('123456')
    expect(screen.getByTestId('value')).toHaveTextContent('123456')

    inputs[2].focus()
    await user.keyboard('{Backspace}')

    expect(screen.getByTestId('value')).toHaveTextContent('12')
    // The digits that remain must be the ones that were there, in place.
    expect(inputs[0]).toHaveValue('1')
    expect(inputs[1]).toHaveValue('2')
    expect(inputs[2]).toHaveValue('')
    expect(inputs[3]).toHaveValue('')
    expect(inputs[2]).toHaveFocus()
  })

  it('moves with the arrow keys', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Host />)
    const inputs = cells()

    inputs[3].focus()
    await user.keyboard('{ArrowLeft}')
    expect(inputs[2]).toHaveFocus()
    await user.keyboard('{ArrowRight}')
    expect(inputs[3]).toHaveFocus()
  })

  it('does not move past either end', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Host />)
    const inputs = cells()

    inputs[0].focus()
    await user.keyboard('{ArrowLeft}')
    expect(inputs[0]).toHaveFocus()

    inputs[OTP_LENGTH - 1].focus()
    await user.keyboard('{ArrowRight}')
    expect(inputs[OTP_LENGTH - 1]).toHaveFocus()
  })

  // Authenticator apps show "123 456", mail clients linkify "123-456", password
  // managers paste "123456". All three must fill the field.
  it.each([
    ['plain', '123456'],
    ['spaced', '123 456'],
    ['hyphenated', '123-456'],
    ['padded', '  123456\n'],
  ])('accepts a %s pasted code', async (_name, pasted) => {
    const user = userEvent.setup()
    renderWithProviders(<Host />)
    const inputs = cells()

    inputs[0].focus()
    await user.paste(pasted)

    expect(screen.getByTestId('value')).toHaveTextContent('123456')
  })

  it('truncates an over-long paste to six digits', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Host />)
    cells()[0].focus()
    await user.paste('1234567890')
    expect(screen.getByTestId('value')).toHaveTextContent('123456')
  })

  it('ignores a paste with no digits in it', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Host />)
    cells()[0].focus()
    await user.paste('no digits here')
    expect(screen.getByTestId('value')).toBeEmptyDOMElement()
  })

  // The single most important behaviour in this file. onComplete drives an
  // auto-submit, and the code is single-use: firing twice means the second
  // request always fails and paints an error over a login that succeeded.
  it('fires onComplete exactly once for a typed code', async () => {
    const user = userEvent.setup()
    const onComplete = vi.fn()
    renderWithProviders(<Host onComplete={onComplete} />)
    const inputs = cells()

    for (let i = 0; i < OTP_LENGTH; i++) {
      await user.type(inputs[i], String(i + 1))
    }

    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1))
    expect(onComplete).toHaveBeenCalledWith('123456')
  })

  it('fires onComplete exactly once for a pasted code', async () => {
    const user = userEvent.setup()
    const onComplete = vi.fn()
    renderWithProviders(<Host onComplete={onComplete} />)

    cells()[0].focus()
    await user.paste('123456')

    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1))
    expect(onComplete).toHaveBeenCalledWith('123456')
  })

  // After a rejected code the screen clears the field; retyping must be able to
  // fire again, or the second attempt silently never submits.
  it('fires again after the field is cleared and refilled', async () => {
    const user = userEvent.setup()
    const onComplete = vi.fn()
    renderWithProviders(<Host onComplete={onComplete} />)

    cells()[0].focus()
    await user.paste('111111')
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1))

    // Clear via backspaces, then paste a different code.
    for (let i = 0; i < OTP_LENGTH; i++) await user.keyboard('{Backspace}{Backspace}')
    cells()[0].focus()
    await user.paste('222222')

    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(2))
    expect(onComplete).toHaveBeenLastCalledWith('222222')
  })

  it('marks every cell invalid when the code was rejected', () => {
    renderWithProviders(
      <OtpInput value="123456" onChange={() => {}} invalid />,
    )
    cells().forEach((el) => expect(el).toHaveAttribute('aria-invalid', 'true'))
  })

  it('disables every cell while a verification is in flight', () => {
    renderWithProviders(<OtpInput value="" onChange={() => {}} disabled />)
    cells().forEach((el) => expect(el).toBeDisabled())
  })
})

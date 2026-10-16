import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { PasswordBlock } from './PasswordBlock'
import { renderWithProviders, testAdminSession } from '../../test/renderWithProviders'

const features = (testAdminSession as { features: object }).features
const anonymous = { status: 'anonymous', features } as never

function render(props: Partial<React.ComponentProps<typeof PasswordBlock>> = {}) {
  const onPasswordChange = vi.fn()
  const onConfirmChange = vi.fn()
  const view = renderWithProviders(
    <PasswordBlock
      passwordId="fx-test-password"
      confirmId="fx-test-confirm"
      password=""
      confirm=""
      minLen={10}
      onPasswordChange={onPasswordChange}
      onConfirmChange={onConfirmChange}
      {...props}
    />,
    { session: anonymous },
  )
  return { ...view, onPasswordChange, onConfirmChange }
}

describe('PasswordBlock', () => {
  it('renders both fields with the given ids and the min-length hint', () => {
    render()
    expect(document.getElementById('fx-test-password')).not.toBeNull()
    expect(document.getElementById('fx-test-confirm')).not.toBeNull()
    expect(screen.getByText(/10/)).toBeInTheDocument()
  })

  it('shows the strength meter only once a password exists', () => {
    const { container, rerender } = render()
    expect(container.querySelector('.fx-pwstrength')).toBeNull()
    rerender(
      <PasswordBlock
        passwordId="fx-test-password"
        confirmId="fx-test-confirm"
        password="Str0ng!pass"
        confirm=""
        minLen={10}
        onPasswordChange={() => {}}
        onConfirmChange={() => {}}
      />,
    )
    expect(container.querySelector('.fx-pwstrength')).not.toBeNull()
  })

  it('marks the confirm field invalid on mismatch and shows the hint only when asked', () => {
    render({ password: 'abc', confirm: 'abd', showMismatchHint: true })
    expect(document.getElementById('fx-test-confirm')).toHaveAttribute('aria-invalid', 'true')
    expect(document.querySelector('.fx-auth-hint-error')).not.toBeNull()
  })

  it('hides the mismatch hint by default (InviteScreen parity)', () => {
    render({ password: 'abc', confirm: 'abd' })
    expect(document.getElementById('fx-test-confirm')).toHaveAttribute('aria-invalid', 'true')
    expect(document.querySelector('.fx-auth-hint-error')).toBeNull()
  })

  it('forwards typing to the change handlers', async () => {
    const { onPasswordChange } = render()
    await userEvent.type(document.getElementById('fx-test-password') as HTMLElement, 'a')
    expect(onPasswordChange).toHaveBeenCalledWith('a')
  })
})

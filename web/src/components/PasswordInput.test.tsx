import { describe, it, expect, afterEach, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { PasswordInput } from './PasswordInput'
import { renderWithProviders } from '../test/renderWithProviders'

afterEach(() => vi.restoreAllMocks())

const nameShow = /show password/i

describe('PasswordInput', () => {
  it('starts hidden', () => {
    renderWithProviders(<PasswordInput aria-label="pw" />)
    expect(screen.getByLabelText('pw')).toHaveAttribute('type', 'password')
    expect(screen.getByRole('button', { name: nameShow })).toHaveAttribute('aria-pressed', 'false')
  })

  it('reveals and hides again on the toggle', async () => {
    const user = userEvent.setup()
    renderWithProviders(<PasswordInput aria-label="pw" />)
    const toggle = screen.getByRole('button', { name: nameShow })

    await user.click(toggle)
    expect(screen.getByLabelText('pw')).toHaveAttribute('type', 'text')
    expect(screen.getByRole('button', { name: /hide password/i })).toHaveAttribute(
      'aria-pressed',
      'true',
    )

    await user.click(screen.getByRole('button', { name: /hide password/i }))
    expect(screen.getByLabelText('pw')).toHaveAttribute('type', 'password')
  })

  // Inside a <form> the default button type is `submit`: without this the first
  // click on the eye would post the login form with an empty password.
  it('does not submit the form it sits in', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault())
    renderWithProviders(
      <form onSubmit={onSubmit}>
        <PasswordInput aria-label="pw" />
      </form>,
    )

    await user.click(screen.getByRole('button', { name: nameShow }))

    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByLabelText('pw')).toHaveAttribute('type', 'text')
  })

  // Keeping the toggle out of the tab order was tried and reverted: a reveal a
  // keyboard-only user cannot reach withholds it from exactly the people most
  // likely to need it. Asserted behaviourally, because the attribute alone says
  // nothing about where focus actually lands.
  it('is reachable from the field by Tab', async () => {
    const user = userEvent.setup()
    renderWithProviders(<PasswordInput aria-label="pw" />)

    screen.getByLabelText('pw').focus()
    await user.tab()

    expect(screen.getByRole('button', { name: nameShow })).toHaveFocus()
  })

  // Added this session with the strongest "why" in the component and no test:
  // a revealed field is `type="text"`, and Chrome's Enhanced Spell Check and
  // editor extensions send a text field's contents to a remote service. This is
  // precisely the kind of attribute that goes silently missing.
  it('suppresses spell-check and editor extensions on the revealed value', async () => {
    const user = userEvent.setup()
    renderWithProviders(<PasswordInput aria-label="pw" />)
    const field = screen.getByLabelText('pw')

    const assertSuppressed = () => {
      expect(field).toHaveAttribute('spellcheck', 'false')
      expect(field).toHaveAttribute('autocorrect', 'off')
      expect(field).toHaveAttribute('autocapitalize', 'off')
      expect(field).toHaveAttribute('data-gramm', 'false')
    }

    assertSuppressed()
    await user.click(screen.getByRole('button', { name: nameShow }))
    expect(field).toHaveAttribute('type', 'text') // the state that makes it matter
    assertSuppressed()
  })

  // The toggle's aria-controls points at the field, and twelve call sites rely
  // on their own `id` reaching the input so `<AuthField htmlFor>` still binds.
  it('uses the caller id, and generates a distinct one per field otherwise', () => {
    const { unmount } = renderWithProviders(<PasswordInput id="mine" aria-label="pw" />)
    expect(screen.getByLabelText('pw')).toHaveAttribute('id', 'mine')
    expect(screen.getByRole('button', { name: nameShow })).toHaveAttribute('aria-controls', 'mine')
    unmount()

    renderWithProviders(
      <>
        <PasswordInput aria-label="a" />
        <PasswordInput aria-label="b" />
      </>,
    )
    const a = screen.getByLabelText('a').id
    const b = screen.getByLabelText('b').id
    expect(a).toBeTruthy()
    expect(a).not.toBe(b)
    const toggles = screen.getAllByRole('button', { name: nameShow })
    expect(toggles.map((t) => t.getAttribute('aria-controls'))).toEqual([a, b])
  })

  // `name` reaches password managers; `autoFocus` is used at six call sites.
  it('forwards name and autoFocus', () => {
    renderWithProviders(<PasswordInput aria-label="pw" name="password" autoFocus />)
    expect(screen.getByLabelText('pw')).toHaveAttribute('name', 'password')
    expect(screen.getByLabelText('pw')).toHaveFocus()
  })

  // A revealed password must never survive a remount — otherwise leaving and
  // returning to a screen would put someone's password on display for them.
  it('starts hidden again after a remount', async () => {
    const user = userEvent.setup()
    const { unmount } = renderWithProviders(<PasswordInput aria-label="pw" />)
    await user.click(screen.getByRole('button', { name: nameShow }))
    expect(screen.getByLabelText('pw')).toHaveAttribute('type', 'text')
    unmount()

    renderWithProviders(<PasswordInput aria-label="pw" />)
    expect(screen.getByLabelText('pw')).toHaveAttribute('type', 'password')
  })

  it('forwards the caller autoComplete and value plumbing', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    renderWithProviders(
      <PasswordInput aria-label="pw" autoComplete="new-password" value="" onChange={onChange} />,
    )

    expect(screen.getByLabelText('pw')).toHaveAttribute('autocomplete', 'new-password')
    await user.type(screen.getByLabelText('pw'), 'x')
    expect(onChange).toHaveBeenCalled()
  })
})

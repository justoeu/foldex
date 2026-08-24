import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CreateUserDialog } from './CreateUserDialog'
import { freshState, installAxiosMock, type MockState } from '../../test/server'
import { http } from '../../api/client'
import { GENERATED_LENGTH, GENERATED_MAX_LENGTH } from '../../lib/generatePassword'
import { renderWithProviders } from '../../test/renderWithProviders'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})
afterEach(() => vi.restoreAllMocks())

/** navigator.clipboard is getter-only in jsdom, so it has to be redefined. */
function stubClipboard(writeText: ReturnType<typeof vi.fn>) {
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText },
    configurable: true,
    writable: true,
  })
  return writeText
}

const emailField = () => screen.getByLabelText(/^e-mail$/i)
const passwordField = () => screen.getByLabelText(/^temporary password$/i)
const confirmField = () => screen.getByLabelText(/^confirm the temporary password$/i)
const submitButton = () => screen.getByRole('button', { name: /add user/i })
const generateButton = () => screen.getByRole('button', { name: /generate a password/i })

describe('CreateUserDialog — confirmation', () => {
  it('refuses to submit while the two typed passwords differ', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await user.type(emailField(), 'nova@foldex.test')
    await user.type(passwordField(), 'correct-horse-battery')
    await user.type(confirmField(), 'correct-horse-batteryX')

    expect(await screen.findByText(/do not match/i)).toBeInTheDocument()
    expect(submitButton()).toBeDisabled()
  })

  it('stays blocked while the confirmation is still empty', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await user.type(emailField(), 'nova@foldex.test')
    await user.type(passwordField(), 'correct-horse-battery')

    expect(submitButton()).toBeDisabled()
    // Nothing is said yet: the field is untouched, not wrong.
    expect(screen.queryByText(/do not match/i)).toBeNull()
  })

  it('submits the typed password once both fields agree', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    renderWithProviders(<CreateUserDialog onClose={onClose} />)

    await user.type(emailField(), 'nova@foldex.test')
    await user.type(passwordField(), 'correct-horse-battery')
    await user.type(confirmField(), 'correct-horse-battery')

    await waitFor(() => expect(submitButton()).toBeEnabled())
    await user.click(submitButton())

    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(state.adminCreatedUsers).toEqual([
      { email: 'nova@foldex.test', name: '', password: 'correct-horse-battery', role: 'editor' },
    ])
  })
})

describe('CreateUserDialog — generated password', () => {
  it('shows the generated value in clear, because it has to be handed over', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())

    const shown = await screen.findByTestId('generated-password')
    expect(shown.textContent).toHaveLength(GENERATED_LENGTH)
    // The field holds the same value the band displays.
    expect(passwordField()).toHaveValue(shown.textContent)
  })

  it('drops the confirmation field, since a generated value has no typo to catch', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await user.type(emailField(), 'nova@foldex.test')
    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())

    expect(screen.queryByLabelText(/^confirm the temporary password$/i)).toBeNull()
    await waitFor(() => expect(submitButton()).toBeEnabled())
  })

  it('generates against the INSTANCE floor, not the compiled-in one', async () => {
    state.passwordMinLength = 32
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())

    // 20 would be refused by an instance demanding 32 — a button that cannot
    // work is the defect this guards.
    expect((await screen.findByTestId('generated-password')).textContent).toHaveLength(32)
  })

  it('does not offer to generate before the floor is known', () => {
    // The policy query has not resolved on the first paint.
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)
    expect(generateButton()).toBeDisabled()
  })

  it('brings the confirmation back the moment the value is edited by hand', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await user.type(emailField(), 'nova@foldex.test')
    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())
    await screen.findByTestId('generated-password')

    await user.type(passwordField(), 'x')

    expect(screen.queryByTestId('generated-password')).toBeNull()
    expect(confirmField()).toBeInTheDocument()
    expect(submitButton()).toBeDisabled()
  })

  it('copies the generated value, and survives a clipboard that refuses', async () => {
    const user = userEvent.setup()
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    stubClipboard(writeText)
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())
    const value = (await screen.findByTestId('generated-password')).textContent

    await user.click(screen.getByRole('button', { name: /^copy$/i }))
    expect(writeText).toHaveBeenCalledWith(value)
    // The refusal is silent: the value is on screen either way.
    expect(screen.getByRole('button', { name: /^copy$/i })).toBeInTheDocument()

    writeText.mockResolvedValue(undefined)
    await user.click(screen.getByRole('button', { name: /^copy$/i }))
    expect(await screen.findByRole('button', { name: /^copied$/i })).toBeInTheDocument()
  })

  it('submits the generated password verbatim', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await user.type(emailField(), 'nova@foldex.test')
    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())
    const value = (await screen.findByTestId('generated-password')).textContent

    await waitFor(() => expect(submitButton()).toBeEnabled())
    await user.click(submitButton())

    await waitFor(() => expect(state.adminCreatedUsers?.[0]?.password).toBe(value))
  })
  it('still generates when the policy query FAILS, on the compiled-in floor', async () => {
    // The fallback is stated in the code and was unlocked: swapping the gate to
    // `!policy.data` left Generate permanently dead on any instance whose
    // policy fetch fails, with the whole suite green.
    const real = http.get.bind(http)
    vi.spyOn(http, 'get').mockImplementation((async (url: string, ...rest: unknown[]) => {
      if (url.startsWith('/api/admin/policy')) throw new Error('boom')
      return (real as never as (...a: unknown[]) => unknown)(url, ...rest)
    }) as never)

    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())
    expect((await screen.findByTestId('generated-password')).textContent)
      .toHaveLength(GENERATED_LENGTH)
  })

  it('leaves the confirmation EMPTY when it had been filled before generating', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await user.type(passwordField(), 'typed-by-hand-first')
    await user.type(confirmField(), 'typed-by-hand-first')
    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())
    await screen.findByTestId('generated-password')

    // Editing by hand brings the field back; it must not carry the value the
    // administrator typed for the PREVIOUS password.
    await user.type(passwordField(), 'x')
    expect(confirmField()).toHaveValue('')
  })

  it('lets the SERVER refuse a floor no password can reach', async () => {
    // A floor above 72 can no longer be SET (MaxPasswordFloor is bcrypt's
    // truncation point now), but one stored before that bound tightened is
    // still honoured on read — maxStoredPasswordFloor is 128. Gating on the
    // raw floor would make both a typed and a generated value un-submittable
    // on such an instance: a dead button in place of the one answer that names
    // the real number.
    state.passwordMinLength = 100
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await user.type(emailField(), 'nova@foldex.test')
    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())

    expect((await screen.findByTestId('generated-password')).textContent)
      .toHaveLength(GENERATED_MAX_LENGTH)
    await waitFor(() => expect(submitButton()).toBeEnabled())
  })

  it('states the instance floor on screen, not only in the disabled button', async () => {
    state.passwordMinLength = 24
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)
    expect(await screen.findByText(/at least 24 characters/i)).toBeInTheDocument()
  })

  it('does not mark its own output deficient with a strength checklist', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await user.type(passwordField(), 'typed')
    expect(screen.getByText(/a symbol/i)).toBeInTheDocument()

    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())
    expect(screen.queryByText(/a symbol/i)).toBeNull()
  })

  it('keeps the plaintext out of page translation', async () => {
    // Chrome/Edge upload the page's visible text to translate.googleapis.com.
    const user = userEvent.setup()
    renderWithProviders(<CreateUserDialog onClose={vi.fn()} />)

    await waitFor(() => expect(generateButton()).toBeEnabled())
    await user.click(generateButton())
    expect(await screen.findByTestId('generated-password'))
      .toHaveAttribute('translate', 'no')
  })
})

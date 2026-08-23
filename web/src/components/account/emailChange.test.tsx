import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AccountPage } from '../../pages/AccountPage'
import { renderWithProviders, testAdminUser } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import type { AuthUser, SessionState } from '../../auth/types'

afterEach(() => vi.restoreAllMocks())

function sessionWith(over: Partial<AuthUser>): SessionState {
  return {
    status: 'authenticated',
    user: { ...testAdminUser, ...over },
    csrfToken: 'test-csrf-token',
    features: { google_oauth: false, two_factor: true, email_delivery: true },
  }
}

/** Seeds both reads the panel makes: the identities list and the pending change. */
function mockReads(pending: { new_email: string; expires_at: string } | null) {
  return vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
    if (url === '/api/auth/email/change') return { data: { pending } } as never
    return { data: { identities: [] } } as never
  })
}

function render(session: SessionState) {
  return renderWithProviders(<AccountPage initialTab="access" />, { session })
}

const emailRow = () => within(screen.getByRole('group', { name: /^e-mail$/i }))

describe('changing the account e-mail', () => {
  it('shows the current address and its verified state', async () => {
    mockReads(null)
    render(sessionWith({ email: 'me@example.com', email_verified_at: '2026-01-01T00:00:00Z' }))

    await waitFor(() => expect(emailRow().getByText('me@example.com')).toBeInTheDocument())
    expect(emailRow().getByText('Verified')).toBeInTheDocument()
  })

  // The step-up. Without the password a stolen session moves the account's
  // recovery channel to an address the attacker owns.
  it('will not send the confirmation without the current password', async () => {
    mockReads(null)
    render(sessionWith({}))
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /change e-mail/i }))
    await user.type(emailRow().getByLabelText(/new e-mail/i), 'new@example.com')

    const send = emailRow().getByRole('button', { name: /send confirmation/i })
    expect(send).toBeDisabled()

    await user.type(emailRow().getByLabelText(/current password/i), 'hunter2hunter2')
    expect(send).toBeEnabled()
  })

  it('sends the request and says the current address still works', async () => {
    mockReads(null)
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { new_email: 'new@example.com', expires_at: '2099-01-01T00:00:00Z' },
    } as never)
    render(sessionWith({}))
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /change e-mail/i }))
    await user.type(emailRow().getByLabelText(/new e-mail/i), 'new@example.com')
    await user.type(emailRow().getByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(emailRow().getByRole('button', { name: /send confirmation/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/email/change', {
        new_email: 'new@example.com',
        password: 'hunter2hunter2',
      }),
    )
  })

  // A live request outranks the form: the next useful action is opening the
  // link or cancelling, not typing a third address.
  it('reports a pending change instead of offering the form again', async () => {
    mockReads({ new_email: 'new@example.com', expires_at: '2099-01-01T00:00:00Z' })
    render(sessionWith({}))

    await waitFor(() =>
      expect(emailRow().getByText(/we sent a confirmation link to new@example.com/i)).toBeInTheDocument())
    expect(emailRow().queryByRole('button', { name: /change e-mail/i })).not.toBeInTheDocument()
    expect(emailRow().getByRole('button', { name: /cancel the change/i })).toBeInTheDocument()
  })

  it('cancels a pending change', async () => {
    mockReads({ new_email: 'new@example.com', expires_at: '2099-01-01T00:00:00Z' })
    const del = vi.spyOn(http, 'delete').mockResolvedValue({ status: 204 } as never)
    render(sessionWith({}))
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /cancel the change/i }))
    await waitFor(() => expect(del).toHaveBeenCalledWith('/api/auth/email/change'))
  })

  // The refusal has to name the cause: "another account uses that address" is
  // something the user can act on, and a generic failure is not.
  it('says when the address belongs to another account', async () => {
    mockReads(null)
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'email_taken' } } },
    })
    render(sessionWith({}))
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /change e-mail/i }))
    await user.type(emailRow().getByLabelText(/new e-mail/i), 'taken@example.com')
    await user.type(emailRow().getByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(emailRow().getByRole('button', { name: /send confirmation/i }))

    expect(await screen.findByText(/another account already uses that address/i)).toBeInTheDocument()
    // The address survives a refusal — retyping it is the user's time — but the
    // password does not: left in the field it is one autofill from being resent.
    expect(emailRow().getByLabelText(/new e-mail/i)).toHaveValue('taken@example.com')
    expect(emailRow().getByLabelText(/current password/i)).toHaveValue('')
  })

  it('says when the instance cannot send e-mail at all', async () => {
    mockReads(null)
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 503, data: { error: { code: 'mail_unavailable' } } },
    })
    render(sessionWith({}))
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /change e-mail/i }))
    await user.type(emailRow().getByLabelText(/new e-mail/i), 'new@example.com')
    await user.type(emailRow().getByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(emailRow().getByRole('button', { name: /send confirmation/i }))

    expect(await screen.findByText(/cannot send e-mail/i)).toBeInTheDocument()
  })
})

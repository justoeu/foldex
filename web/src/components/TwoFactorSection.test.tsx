import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TwoFactorSection } from './TwoFactorSection'
import { renderWithProviders } from '../test/renderWithProviders'
import { http } from '../api/client'

afterEach(() => vi.restoreAllMocks())

type Status = { enabled: boolean; recovery_codes_remaining: number; required: boolean }

function mockStatus(s: Status) {
  return vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
    if (url === '/api/auth/2fa') return { data: s } as never
    return { data: {} } as never
  })
}

describe('TwoFactorSection', () => {
  it('offers to turn it on when it is off', async () => {
    mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
    renderWithProviders(<TwoFactorSection />)

    expect(await screen.findByText(/two-step verification is off/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /turn on two-step/i })).toBeInTheDocument()
  })

  // Adding a second factor with nothing but a live session would let an
  // attacker holding a stolen cookie lock the real owner out of their account.
  it('will not start enrollment without the password', async () => {
    mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
    renderWithProviders(<TwoFactorSection />)

    const button = await screen.findByRole('button', { name: /turn on two-step/i })
    expect(button).toBeDisabled()

    await userEvent.setup().type(screen.getByLabelText(/current password/i), 'hunter2hunter2')
    expect(button).toBeEnabled()
  })

  it('shows the QR and the setup key once enrollment starts', async () => {
    const user = userEvent.setup()
    mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
    vi.spyOn(http, 'post').mockResolvedValue({
      data: {
        secret: 'JBSWY3DPEHPK3PXP',
        otpauth: 'otpauth://totp/x',
        issuer: 'Foldex',
        account: 'a@b.test',
        qr_url: '/api/auth/2fa/totp/qr.png',
      },
    } as never)

    renderWithProviders(<TwoFactorSection />)
    await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: /turn on two-step/i }))

    const img = await screen.findByAltText(/qr code/i)
    expect(img).toHaveAttribute('src', '/api/auth/2fa/totp/qr.png')
    // The typed key must be available too — a user on a desktop with no phone
    // camera has no other way in.
    expect(screen.getByText('JBSWY3DPEHPK3PXP')).toBeInTheDocument()
  })

  it('keeps enrollment open and clears a rejected confirmation code', async () => {
    const user = userEvent.setup()
    mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
    vi.spyOn(http, 'post').mockImplementation(async (url: string) => {
      if (url.endsWith('/start')) {
        return { data: {
          secret: 'JBSWY3DPEHPK3PXP', otpauth: 'otpauth://totp/x', issuer: 'Foldex',
          account: 'a@b.test', qr_url: '/api/auth/2fa/totp/qr.png',
        } } as never
      }
      throw { response: { status: 401, data: { error: { code: 'invalid_code' } } } }
    })
    renderWithProviders(<TwoFactorSection />)
    await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: /turn on two-step/i }))
    await screen.findByAltText(/qr code/i)
    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await user.click(screen.getByRole('button', { name: /turn on two-step/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/not valid/i)
    expect(screen.getByAltText(/qr code/i)).toBeInTheDocument()
    expect(cells.every((cell) => cell.value === '')).toBe(true)
  })

  it('moves from enrollment to one-time recovery codes after confirmation', async () => {
    const user = userEvent.setup()
    mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
    vi.spyOn(http, 'post').mockImplementation(async (url: string) => {
      if (url.endsWith('/start')) {
        return { data: {
          secret: 'JBSWY3DPEHPK3PXP', otpauth: 'otpauth://totp/x', issuer: 'Foldex',
          account: 'a@b.test', qr_url: '/api/auth/2fa/totp/qr.png',
        } } as never
      }
      return { data: { recovery_codes: ['AAAA-BBBB'] } } as never
    })
    renderWithProviders(<TwoFactorSection />)
    await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: /turn on two-step/i }))
    await screen.findByAltText(/qr code/i)
    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await user.click(screen.getByRole('button', { name: /turn on two-step/i }))

    expect(await screen.findByTestId('recovery-codes')).toBeInTheDocument()
    expect(screen.getByText('AAAA-BBBB')).toBeInTheDocument()
    expect(screen.queryByAltText(/qr code/i)).not.toBeInTheDocument()
  })

  it('cancels enrollment and clears the staged password and code', async () => {
    const user = userEvent.setup()
    mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
    vi.spyOn(http, 'post').mockResolvedValue({
      data: {
        secret: 'JBSWY3DPEHPK3PXP', otpauth: 'otpauth://totp/x', issuer: 'Foldex',
        account: 'a@b.test', qr_url: '/api/auth/2fa/totp/qr.png',
      },
    } as never)
    renderWithProviders(<TwoFactorSection />)
    const password = await screen.findByLabelText(/current password/i)
    await user.type(password, 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: /turn on two-step/i }))
    await screen.findByAltText(/qr code/i)
    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await user.click(screen.getByRole('button', { name: /cancel/i }))

    expect(screen.queryByAltText(/qr code/i)).not.toBeInTheDocument()
    expect(password).toHaveValue('')
    expect(screen.getByRole('button', { name: /turn on two-step/i })).toBeDisabled()
  })

  it('reports how many recovery codes remain', async () => {
    mockStatus({ enabled: true, recovery_codes_remaining: 3, required: false })
    renderWithProviders(<TwoFactorSection />)
    expect(await screen.findByText(/3 recovery codes left/i)).toBeInTheDocument()
  })

  it('uses the singular for one remaining code', async () => {
    mockStatus({ enabled: true, recovery_codes_remaining: 1, required: false })
    renderWithProviders(<TwoFactorSection />)
    expect(await screen.findByText(/1 recovery code left/i)).toBeInTheDocument()
  })

  // The server refuses an admin's disable with 403; hiding the button as well
  // means the user never reaches a dead end the UI implied was open.
  it('hides the disable button when the policy forbids it', async () => {
    mockStatus({ enabled: true, recovery_codes_remaining: 10, required: true })
    renderWithProviders(<TwoFactorSection />)

    expect(await screen.findByText(/administrators cannot turn this off/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /turn off two-step/i })).not.toBeInTheDocument()
    // Regenerating codes stays available: it is not a way around the policy.
    expect(screen.getByRole('button', { name: /generate new recovery codes/i })).toBeInTheDocument()
  })

  it('offers to disable it for a non-admin', async () => {
    mockStatus({ enabled: true, recovery_codes_remaining: 10, required: false })
    renderWithProviders(<TwoFactorSection />)
    expect(await screen.findByRole('button', { name: /turn off two-step/i })).toBeInTheDocument()
  })

  // Turning it off needs BOTH proofs — the same two that switched it on.
  it('requires a password and a code before disabling', async () => {
    const user = userEvent.setup()
    mockStatus({ enabled: true, recovery_codes_remaining: 10, required: false })
    renderWithProviders(<TwoFactorSection />)

    const off = await screen.findByRole('button', { name: /turn off two-step/i })
    expect(off).toBeDisabled()

    await user.type(screen.getByLabelText(/current password/i), 'hunter2hunter2')
    expect(off).toBeDisabled()

    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await waitFor(() => expect(off).toBeEnabled())
  })

  it('does not disable when the destructive confirmation is canceled', async () => {
    const user = userEvent.setup()
    mockStatus({ enabled: true, recovery_codes_remaining: 10, required: false })
    const post = vi.spyOn(http, 'post')
    renderWithProviders(<TwoFactorSection />)
    await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await user.click(screen.getByRole('button', { name: /turn off two-step/i }))
    const confirmDialog = await screen.findByRole('dialog', { name: /turn off two-step/i })
    await user.click(within(confirmDialog).getByRole('button', { name: /cancel/i }))

    expect(post).not.toHaveBeenCalled()
    expect(screen.getByText(/two-step verification is on/i)).toBeInTheDocument()
  })

  it('disables after confirmation and transitions back to the off state', async () => {
    const user = userEvent.setup()
    let enabled = true
    vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
      if (url === '/api/auth/2fa') {
        return { data: { enabled, recovery_codes_remaining: enabled ? 10 : 0, required: false } } as never
      }
      return { data: {} } as never
    })
    const post = vi.spyOn(http, 'post').mockImplementation(async (url: string) => {
      if (url.endsWith('/disable')) enabled = false
      return { data: {} } as never
    })
    renderWithProviders(<TwoFactorSection />)
    const password = await screen.findByLabelText(/current password/i)
    await user.type(password, 'hunter2hunter2')
    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await user.click(screen.getByRole('button', { name: /turn off two-step/i }))
    const confirmDialog = await screen.findByRole('dialog', { name: /turn off two-step/i })
    await user.click(within(confirmDialog).getByRole('button', { name: /^confirm$/i }))

    expect(await screen.findByText(/two-step verification is off/i)).toBeInTheDocument()
    expect(password).toHaveValue('')
    expect(post).toHaveBeenCalledWith('/api/auth/2fa/totp/disable', {
      password: 'hunter2hunter2',
      code: '123456',
    })
  })

  it('shows the recovery codes after regenerating them', async () => {
    const user = userEvent.setup()
    mockStatus({ enabled: true, recovery_codes_remaining: 2, required: false })
    vi.spyOn(http, 'post').mockResolvedValue({
      data: { recovery_codes: ['AAAAA-BBBBB', 'CCCCC-DDDDD'] },
    } as never)

    renderWithProviders(<TwoFactorSection />)
    await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await user.click(screen.getByRole('button', { name: /generate new recovery codes/i }))

    expect(await screen.findByTestId('recovery-codes')).toBeInTheDocument()
    expect(screen.getByText('AAAAA-BBBBB')).toBeInTheDocument()
    // Continuing is gated behind an explicit acknowledgement: this is the only
    // time the codes are ever shown.
    expect(screen.getByRole('button', { name: /continue/i })).toBeDisabled()
    await user.click(screen.getByRole('checkbox'))
    expect(screen.getByRole('button', { name: /continue/i })).toBeEnabled()
    await user.click(screen.getByRole('button', { name: /continue/i }))
    expect(await screen.findByText(/two-step verification is on/i)).toBeInTheDocument()
    expect(screen.queryByTestId('recovery-codes')).not.toBeInTheDocument()
  })

  it('keeps 2FA enabled and clears a rejected regeneration code', async () => {
    const user = userEvent.setup()
    mockStatus({ enabled: true, recovery_codes_remaining: 2, required: false })
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 401, data: { error: { code: 'invalid_code' } } },
    })
    renderWithProviders(<TwoFactorSection />)
    await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await user.click(screen.getByRole('button', { name: /generate new recovery codes/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/not valid/i)
    expect(screen.getByText(/two-step verification is on/i)).toBeInTheDocument()
    expect(cells.every((cell) => cell.value === '')).toBe(true)
  })

  it('reports a wrong password without a generic error', async () => {
    const user = userEvent.setup()
    mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
    vi.spyOn(http, 'post').mockImplementation(() =>
      Promise.reject({
        response: { status: 401, data: { error: { code: 'invalid_credentials' } } },
      }) as never,
    )

    renderWithProviders(<TwoFactorSection />)
    await user.type(await screen.findByLabelText(/current password/i), 'wrong-password')
    await user.click(screen.getByRole('button', { name: /turn on two-step/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/password is incorrect/i)
  })
})

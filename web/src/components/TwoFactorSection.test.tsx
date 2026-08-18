import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TwoFactorSection } from './TwoFactorSection'
import { renderWithProviders } from '../test/renderWithProviders'
import { http } from '../api/client'

afterEach(() => vi.restoreAllMocks())

type Status = {
  enabled: boolean
  recovery_codes_remaining: number
  required: boolean
  totp_enabled?: boolean
  email_enabled?: boolean
  can_disable_totp?: boolean
  can_disable_email?: boolean
  email_available?: boolean
}

/**
 * Seeds the status endpoint, defaulting the per-method fields to the shape the
 * server sends for a TOTP-only account on an SMTP instance.
 *
 * The defaults are DERIVED from `enabled` rather than hardcoded, so a test that
 * only cares about "2FA is on" does not silently assert a method mix it never
 * chose — and a test that does care states it.
 */
function mockStatus(s: Status) {
  const full = {
    totp_enabled: s.enabled,
    email_enabled: false,
    can_disable_totp: s.enabled && !s.required,
    can_disable_email: false,
    email_available: true,
    ...s,
  }
  return vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
    if (url === '/api/auth/2fa') return { data: full } as never
    return { data: {} } as never
  })
}

describe('TwoFactorSection', () => {
  it('offers to turn it on when it is off', async () => {
    mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
    renderWithProviders(<TwoFactorSection />)

    expect(await screen.findByText(/two-step verification is off/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /set up an authenticator app/i })).toBeInTheDocument()
  })

  // Adding a second factor with nothing but a live session would let an
  // attacker holding a stolen cookie lock the real owner out of their account.
  it('will not start enrollment without the password', async () => {
    mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
    renderWithProviders(<TwoFactorSection />)

    const button = await screen.findByRole('button', { name: /set up an authenticator app/i })
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
    await user.click(screen.getByRole('button', { name: /set up an authenticator app/i }))

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
    await user.click(screen.getByRole('button', { name: /set up an authenticator app/i }))
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
    await user.click(screen.getByRole('button', { name: /set up an authenticator app/i }))
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
    await user.click(screen.getByRole('button', { name: /set up an authenticator app/i }))
    await screen.findByAltText(/qr code/i)
    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await user.click(screen.getByRole('button', { name: /cancel/i }))

    expect(screen.queryByAltText(/qr code/i)).not.toBeInTheDocument()
    expect(password).toHaveValue('')
    expect(screen.getByRole('button', { name: /set up an authenticator app/i })).toBeDisabled()
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
        return { data: {
          enabled,
          totp_enabled: enabled,
          email_enabled: false,
          recovery_codes_remaining: enabled ? 10 : 0,
          required: false,
          can_disable_totp: enabled,
          can_disable_email: false,
          email_available: true,
        } } as never
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
    await user.click(screen.getByRole('button', { name: /set up an authenticator app/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/password is incorrect/i)
  })

  // ADR-37: e-mail is a factor the account ENROLLS, so the section has to name
  // both methods and act on each independently.
  describe('the e-mail method', () => {
    it('offers e-mail enrollment only where a mailed code could arrive', async () => {
      mockStatus({
        enabled: false, recovery_codes_remaining: 0, required: false, email_available: false,
      })
      renderWithProviders(<TwoFactorSection />)

      await screen.findByRole('button', { name: /set up an authenticator app/i })
      // The server refuses the enrollment on an instance with no SMTP, so a
      // button here would be a promise the backend always breaks.
      expect(screen.queryByRole('button', { name: /set up e-mail codes/i })).not.toBeInTheDocument()
    })

    it('enrolls e-mail and shows the recovery codes it issues', async () => {
      const user = userEvent.setup()
      mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
      const post = vi.spyOn(http, 'post').mockImplementation(async (url: string) => {
        if (url.endsWith('/email/start')) {
          return { data: { account: 'a•••@b.test', expires_in: 300, digits: 6 } } as never
        }
        return { data: { recovery_codes: ['AAAA-BBBB'] } } as never
      })

      renderWithProviders(<TwoFactorSection />)
      await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
      await user.click(screen.getByRole('button', { name: /set up e-mail codes/i }))

      // No QR: the whole point is that this method needs no authenticator.
      expect(await screen.findByText(/a•••@b\.test/)).toBeInTheDocument()
      expect(screen.queryByAltText(/qr code/i)).not.toBeInTheDocument()
      expect(post).toHaveBeenCalledWith('/api/auth/2fa/email/start', { password: 'hunter2hunter2' })

      const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
      cells[0].focus()
      await user.paste('123456')
      await user.click(screen.getByRole('button', { name: /turn on two-step/i }))

      // Mandatory here, not a nicety: an e-mail-only account arriving through a
      // password-reset link is refused the e-mail method on purpose, and these
      // are its only way back in.
      expect(await screen.findByTestId('recovery-codes')).toBeInTheDocument()
      expect(post).toHaveBeenCalledWith('/api/auth/2fa/email/confirm', { code: '123456' })
    })

    it('names both methods once each is on', async () => {
      mockStatus({
        enabled: true, recovery_codes_remaining: 8, required: false,
        totp_enabled: true, email_enabled: true,
        can_disable_totp: true, can_disable_email: true,
      })
      renderWithProviders(<TwoFactorSection />)

      // Scoped to the status list: "authenticator app" also appears on the
      // setup button, so a bare text query would pass without the list existing.
      const methods = await screen.findAllByRole('listitem')
      expect(methods.map((li) => li.textContent)).toEqual(
        expect.arrayContaining([
          expect.stringMatching(/authenticator app/i),
          expect.stringMatching(/e-mail code/i),
        ]),
      )
      expect(screen.getByRole('button', { name: /turn off two-step/i })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /turn off e-mail codes/i })).toBeInTheDocument()
    })

    // The server owns the rule. Re-deriving it here would put a second copy in
    // the browser, free to disagree with the one actually enforced.
    it('hides a disable button the server says is not allowed', async () => {
      mockStatus({
        enabled: true, recovery_codes_remaining: 8, required: true,
        totp_enabled: true, email_enabled: true,
        can_disable_totp: false, can_disable_email: true,
      })
      renderWithProviders(<TwoFactorSection />)

      await screen.findByText(/two-step verification is on/i)
      expect(screen.queryByRole('button', { name: /turn off two-step/i })).not.toBeInTheDocument()
      expect(screen.getByRole('button', { name: /turn off e-mail codes/i })).toBeInTheDocument()
    })

    // Without this, the code field is a box an e-mail-only account cannot fill:
    // it has no authenticator to read six digits from.
    it('offers to mail a step-up code when e-mail is enrolled', async () => {
      const user = userEvent.setup()
      mockStatus({
        enabled: true, recovery_codes_remaining: 8, required: false,
        totp_enabled: false, email_enabled: true,
        can_disable_totp: false, can_disable_email: true,
      })
      const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)

      renderWithProviders(<TwoFactorSection />)
      await user.click(await screen.findByRole('button', { name: /e-mail me a code/i }))

      expect(post).toHaveBeenCalledWith('/api/auth/2fa/email/send')
      expect(await screen.findByText(/code sent to your address/i)).toBeInTheDocument()
    })

    it('does not offer a mailed code to an account without the factor', async () => {
      mockStatus({ enabled: true, recovery_codes_remaining: 8, required: false })
      renderWithProviders(<TwoFactorSection />)

      await screen.findByText(/two-step verification is on/i)
      expect(screen.queryByRole('button', { name: /e-mail me a code/i })).not.toBeInTheDocument()
    })

    // Removing one of two factors keeps the recovery codes, so warning that
    // they will be deleted would be a lie that talks the user out of a safe
    // change.
    it('warns about recovery codes only when the last factor is going', async () => {
      const user = userEvent.setup()
      mockStatus({
        enabled: true, recovery_codes_remaining: 8, required: false,
        totp_enabled: true, email_enabled: true,
        can_disable_totp: true, can_disable_email: true,
      })
      vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)

      renderWithProviders(<TwoFactorSection />)
      await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
      const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
      cells[0].focus()
      await user.paste('123456')
      await user.click(screen.getByRole('button', { name: /turn off e-mail codes/i }))

      const dialog = await screen.findByRole('dialog', { name: /turn off e-mail codes/i })
      expect(dialog).toHaveTextContent(/other method stays on/i)
      expect(dialog).not.toHaveTextContent(/recovery codes will be deleted/i)
    })
  })
})

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
  // `.fx-auth` is the signed-out screen: `position: fixed; inset: 0` with an
  // opaque background. Worn inside a card it paints over the whole card — every
  // label and row background gone, the layout intact underneath — and the
  // repaint storm blocks the main thread, so the screen flickers and never
  // settles. That is what shipped, on this exact component.
  //
  // The CSS is invisible to jsdom (`css: false`), but the CLASS NAME is not:
  // this assertion needed no stylesheet and would have caught it. The
  // stylesheet half is scripts/test-css-auth-overlay.mjs.
  it('never wears the full-screen auth overlay class', async () => {
    mockStatus({ enabled: true, recovery_codes_remaining: 10, required: false })
    const { container } = renderWithProviders(<TwoFactorSection />)
    await screen.findByText(/two-step verification is on/i)

    expect(container.querySelectorAll('.fx-auth')).toHaveLength(0)
    // ...and the replacement is actually there, or the OTP cells are unstyled.
    expect(container.querySelectorAll('.fx-authfield').length).toBeGreaterThan(0)
  })

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
    // Re-queried, not reused: leaving the enrollment swaps the whole panel, so
    // the reference above is a detached node that still carries the old value
    // no matter what the live field shows.
    expect(screen.getByLabelText(/current password/i)).toHaveValue('')
    expect(password).not.toBe(screen.getByLabelText(/current password/i))
    expect(screen.getByRole('button', { name: /set up an authenticator app/i })).toBeDisabled()
  })

  it('reports how many recovery codes remain', async () => {
    mockStatus({ enabled: true, recovery_codes_remaining: 3, required: false })
    renderWithProviders(<TwoFactorSection />)
    expect(await screen.findByText(/3 recovery codes left/i)).toBeInTheDocument()
    // Three is the boundary and it is NOT low. Asserted on the exact value
    // because `<` and `<=` differ only here, and a warning that cries at a full
    // third of the sheet is one people learn to ignore.
    expect(screen.queryByText(/running low/i)).not.toBeInTheDocument()
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

  // The card renders per method, and a method's state, its action and the
  // reason an action is missing all have to sit in the same row. The previous
  // layout put the policy note at the FOOT of the card, four lines below two
  // methods, so it could not say which of them it was about — which is how it
  // was reported: "the buttons are blocked".
  describe('per-method state', () => {
    const rowFor = (name: RegExp) => {
      const row = screen.getAllByRole('group').find((li) => name.test(li.textContent ?? ''))
      if (!row) throw new Error(`no method row matching ${name}`)
      return row
    }

    it('puts the policy note on the method it blocks, not on the card', async () => {
      mockStatus({
        enabled: true, recovery_codes_remaining: 8, required: true,
        totp_enabled: true, email_enabled: true,
        can_disable_totp: false, can_disable_email: true,
      })
      renderWithProviders(<TwoFactorSection />)
      await screen.findByText(/two-step verification is on/i)

      const app = rowFor(/authenticator app/i)
      expect(within(app).getByText(/administrators cannot turn this off/i)).toBeInTheDocument()
      expect(within(app).queryByRole('button', { name: /turn off/i })).not.toBeInTheDocument()

      // ...and the method that CAN be removed keeps its button and says nothing
      // about a policy that does not apply to it.
      const mail = rowFor(/e-mail code/i)
      expect(within(mail).getByRole('button', { name: /turn off e-mail codes/i })).toBeInTheDocument()
      expect(within(mail).queryByText(/administrators cannot turn this off/i)).not.toBeInTheDocument()
    })

    // `can_disable_*` is false for a method that is simply not enrolled, so a
    // lock keyed on that alone would claim every unused method is protected.
    it('never locks a method that is not enrolled', async () => {
      mockStatus({ enabled: false, recovery_codes_remaining: 0, required: true })
      renderWithProviders(<TwoFactorSection />)

      await screen.findByRole('button', { name: /set up an authenticator app/i })
      expect(screen.queryByText(/administrators cannot turn this off/i)).not.toBeInTheDocument()
      expect(screen.queryByText(/only method protecting your account/i)).not.toBeInTheDocument()
    })

    it('says why the e-mail method is unavailable instead of hiding the row', async () => {
      mockStatus({
        enabled: false, recovery_codes_remaining: 0, required: false, email_available: false,
      })
      renderWithProviders(<TwoFactorSection />)
      await screen.findByRole('button', { name: /set up an authenticator app/i })

      const mail = rowFor(/e-mail code/i)
      expect(within(mail).getByText(/cannot send e-mail/i)).toBeInTheDocument()
      expect(within(mail).queryByRole('button')).not.toBeInTheDocument()
    })

    // A row that offers "set up" and "turn off" at the same time describes a
    // state that cannot exist. The SERVER never sends this shape —
    // `can_disable_email` is `Email2FAEnabled && mayRemoveFactor(…)` — so this
    // status is deliberately impossible, and the test says what the row does
    // if the two ever disagree. It became worth asserting only when the layout
    // put both buttons in the SAME row: in the old flat strip at the foot of
    // the card, neither was attached to a method and the pair read as two
    // unrelated actions.
    it('never offers to turn off a method that is not enrolled', async () => {
      mockStatus({
        enabled: true, recovery_codes_remaining: 8, required: false,
        totp_enabled: true, email_enabled: false,
        can_disable_totp: true, can_disable_email: true,
      })
      renderWithProviders(<TwoFactorSection />)
      await screen.findByText(/two-step verification is on/i)

      const mail = rowFor(/e-mail code/i)
      expect(within(mail).getByRole('button', { name: /set up e-mail codes/i })).toBeInTheDocument()
      expect(within(mail).queryByRole('button', { name: /turn off/i })).not.toBeInTheDocument()
    })

    // Running out of recovery codes is only ever discovered when they are
    // already needed, so the count alone is not enough of a warning.
    it('warns when the recovery codes are running low', async () => {
      mockStatus({ enabled: true, recovery_codes_remaining: 2, required: false })
      renderWithProviders(<TwoFactorSection />)

      expect(await screen.findByText(/running low/i)).toBeInTheDocument()
    })

    // The disable buttons had this test and regenerating did not, so dropping
    // `proofMissing` from the recovery band alone left every other gate intact
    // and the suite green — while the one action that MINTS a credential
    // became the only one reachable without proving anything.
    it('disables regenerate until both proofs are given', async () => {
      const user = userEvent.setup()
      mockStatus({ enabled: true, recovery_codes_remaining: 10, required: false })
      renderWithProviders(<TwoFactorSection />)

      const regen = await screen.findByRole('button', { name: /generate new recovery codes/i })
      expect(regen).toBeDisabled()

      await user.type(screen.getByLabelText(/current password/i), 'hunter2hunter2')
      expect(regen).toBeDisabled()

      const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
      cells[0].focus()
      await user.paste('123456')
      await waitFor(() => expect(regen).toBeEnabled())
    })

    it('does not warn while a full set remains', async () => {
      mockStatus({ enabled: true, recovery_codes_remaining: 10, required: false })
      renderWithProviders(<TwoFactorSection />)

      await screen.findByText(/10 recovery codes left/i)
      expect(screen.queryByText(/running low/i)).not.toBeInTheDocument()
    })

    // One task per screen: an enrollment in flight must not leave the method
    // list beside it offering ways out of a step that is not finished.
    it('shows only the enrollment while one is in flight', async () => {
      const user = userEvent.setup()
      mockStatus({ enabled: false, recovery_codes_remaining: 0, required: false })
      vi.spyOn(http, 'post').mockResolvedValue({
        data: {
          secret: 'JBSWY3DPEHPK3PXP', otpauth: 'otpauth://totp/x', issuer: 'Foldex',
          account: 'a@b.test', qr_url: '/api/auth/2fa/totp/qr.png',
        },
      } as never)
      renderWithProviders(<TwoFactorSection />)
      await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
      await user.click(screen.getByRole('button', { name: /set up an authenticator app/i }))

      await screen.findByAltText(/qr code/i)
      // Named rows, not every group: the OTP field is itself a `group`, and it
      // is SUPPOSED to be on this screen — asserting zero would pass only by
      // deleting the field the enrollment cannot be completed without.
      expect(screen.queryByRole('group', { name: /authenticator app/i })).not.toBeInTheDocument()
      expect(screen.queryByRole('group', { name: /e-mail code/i })).not.toBeInTheDocument()
      expect(screen.queryByRole('button', { name: /set up e-mail codes/i })).not.toBeInTheDocument()
    })
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

      // The method rows are rendered before the status query resolves — both
      // exist, reading "not set up". Waiting on the STATUS is what makes the
      // assertions below see the loaded state; `findAllByRole` on the rows
      // resolves on the first tick and silently asserts against the defaults.
      await screen.findByText(/two-step verification is on/i)
      const methods = screen.getAllByRole('group')
      expect(methods.map((li) => li.textContent)).toEqual(
        expect.arrayContaining([
          expect.stringMatching(/authenticator app/i),
          expect.stringMatching(/e-mail code/i),
        ]),
      )
      // Scoped to the two METHOD rows: the recovery-codes row shares the list
      // and carries no state chip, so `every` over the whole list asserts
      // something the UI never promised.
      const named = methods.filter((li) => /authenticator app|e-mail code/i.test(li.textContent ?? ''))
      expect(named).toHaveLength(2)
      expect(named.every((li) => /active/i.test(li.textContent ?? ''))).toBe(true)
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

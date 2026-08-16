import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { EnrollTotpScreen } from './EnrollTotpScreen'
import { renderWithProviders } from '../../test/renderWithProviders'
import { http } from '../../api/client'

/**
 * The mandatory-enrollment screen every administrator meets on their first
 * sign-in once AUTH_REQUIRE_2FA_FOR_ADMINS is on — which is the default. It
 * holds only a pre-auth cookie: there is no session yet, and confirming is what
 * produces one.
 */

const enrollment = {
  secret: 'JBSWY3DPEHPK3PXP',
  otpauth: 'otpauth://totp/Foldex:a@b.test?secret=JBSWY3DPEHPK3PXP',
  issuer: 'Foldex',
  account: 'a@b.test',
  qr_url: '/api/auth/2fa/totp/qr.png',
}

const codes = ['AAAAA-BBBBB', 'CCCCC-DDDDD', 'EEEEE-FFFFF']

afterEach(() => vi.restoreAllMocks())

function mockPost(impl: (url: string) => Promise<unknown>) {
  return vi.spyOn(http, 'post').mockImplementation(impl as never)
}

function render() {
  return renderWithProviders(<EnrollTotpScreen />, { session: null })
}

describe('EnrollTotpScreen', () => {
  it('starts an enrollment on mount and shows the QR', async () => {
    const post = mockPost(async () => ({ data: enrollment }))
    render()

    const img = await screen.findByAltText(/qr code/i)
    expect(img).toHaveAttribute('src', '/api/auth/2fa/totp/qr.png')
    expect(post).toHaveBeenCalledWith('/api/auth/2fa/totp/start', {})
  })

  // Two ways to fire twice: React 19's StrictMode double mount, and a language
  // switch changing `t`'s identity. Either replaces the server-side seed under
  // a user who is already scanning the first QR, and out-of-order responses
  // leave the displayed key disagreeing with what was stored.
  it('starts the enrollment exactly once', async () => {
    const post = mockPost(async () => ({ data: enrollment }))
    render()

    await screen.findByAltText(/qr code/i)
    expect(post).toHaveBeenCalledTimes(1)
  })

  // A desktop user with no phone camera has no other way in.
  it('reveals the setup key on request', async () => {
    const user = userEvent.setup()
    mockPost(async () => ({ data: enrollment }))
    render()

    await screen.findByAltText(/qr code/i)
    expect(screen.queryByTestId('totp-secret')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /cannot scan/i }))
    expect(screen.getByTestId('totp-secret')).toHaveTextContent('JBSWY3DPEHPK3PXP')
  })

  it('confirms with the typed code and shows the recovery codes', async () => {
    const user = userEvent.setup()
    const post = mockPost(async (url: string) => {
      if (url === '/api/auth/2fa/totp/start') return { data: enrollment }
      return { data: { status: 'authenticated', user: {}, features: {}, recovery_codes: codes } }
    })
    render()
    await screen.findByAltText(/qr code/i)

    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/2fa/totp/confirm', { code: '123456' }),
    )
    expect(await screen.findByTestId('recovery-codes')).toBeInTheDocument()
    codes.forEach((c) => expect(screen.getByText(c)).toBeInTheDocument())
  })

  /**
   * The single most important behaviour here.
   *
   * Adopting the session swaps the gate over to <App/>, and the recovery codes
   * are shown exactly once — the server keeps only their keyed digests. Adopting
   * before the user acknowledges them would destroy the only copy that ever
   * exists.
   */
  it('does not enter the app until the recovery codes are acknowledged', async () => {
    const user = userEvent.setup()
    mockPost(async (url: string) => {
      if (url === '/api/auth/2fa/totp/start') return { data: enrollment }
      return { data: { status: 'authenticated', user: {}, features: {}, recovery_codes: codes } }
    })
    render()
    await screen.findByAltText(/qr code/i)

    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await screen.findByTestId('recovery-codes')

    const cont = screen.getByRole('button', { name: /continue/i })
    expect(cont).toBeDisabled()

    await user.click(screen.getByRole('checkbox'))
    expect(cont).toBeEnabled()
  })

  it('reports a wrong code and clears the field', async () => {
    const user = userEvent.setup()
    mockPost(async (url: string) => {
      if (url === '/api/auth/2fa/totp/start') return { data: enrollment }
      throw { response: { status: 401, data: { error: { code: 'invalid_code' } } } }
    })
    render()
    await screen.findByAltText(/qr code/i)

    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('000000')

    expect(await screen.findByRole('alert')).toHaveTextContent(/not valid/i)
    ;(screen.getAllByRole('textbox') as HTMLInputElement[]).forEach((c) =>
      expect(c).toHaveValue(''),
    )
  })

  it('explains an expired challenge', async () => {
    const user = userEvent.setup()
    mockPost(async (url: string) => {
      if (url === '/api/auth/2fa/totp/start') return { data: enrollment }
      throw { response: { status: 401, data: { error: { code: 'challenge_invalid' } } } }
    })
    render()
    await screen.findByAltText(/qr code/i)

    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')

    expect(await screen.findByRole('alert')).toHaveTextContent(/expired/i)
  })

  it('surfaces a failed start instead of spinning forever', async () => {
    mockPost(async () => {
      throw { response: { status: 500, data: {} } }
    })
    render()

    expect(await screen.findByRole('alert')).toHaveTextContent(/could not start/i)
    expect(screen.queryByAltText(/qr code/i)).not.toBeInTheDocument()
  })

  // There is no "skip": the policy exists precisely so an admin password alone
  // is never enough. The only way out is to sign in as someone else.
  it('offers no way to bypass the enrollment', async () => {
    mockPost(async () => ({ data: enrollment }))
    render()
    await screen.findByAltText(/qr code/i)

    expect(screen.queryByRole('button', { name: /skip|later|remind/i })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /sign in as someone else/i })).toBeInTheDocument()
  })
})

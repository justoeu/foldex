import { describe, it, expect, vi, afterEach } from 'vitest'
import { StrictMode, type ReactNode } from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from '../../auth/AuthProvider'
import { makeQueryClient } from '../../test/renderWithProviders'
import { VerifyEmailScreen } from './VerifyEmailScreen'
import { EnrollTotpScreen } from './EnrollTotpScreen'
import { http } from '../../api/client'
import type { SessionState } from '../../auth/types'

/**
 * The screens that fire a request on mount, rendered the way main.tsx actually
 * renders them: inside <StrictMode>.
 *
 * renderWithProviders deliberately does not wrap in StrictMode, which is why
 * every other test in this directory is blind to the failure below — a
 * one-shot ref guard paired with a per-effect `alive` flag. StrictMode runs
 * effect → cleanup → effect. The second pass returns early at the ref, so no
 * new closure exists, and the resolved promise is evaluated against the FIRST
 * closure whose cleanup already set alive = false. The request succeeds and the
 * UI never leaves its loading state — with a single-use token already spent and
 * stripped from the URL, so a reload cannot retry.
 */
const anonymous: SessionState = {
  status: 'anonymous',
  features: { google_oauth: false, two_factor: false, email_delivery: false },
}

function renderStrict(ui: ReactNode) {
  return render(
    <StrictMode>
      <QueryClientProvider client={makeQueryClient()}>
        <AuthProvider initialState={anonymous}>{ui}</AuthProvider>
      </QueryClientProvider>
    </StrictMode>,
  )
}

afterEach(() => vi.restoreAllMocks())

describe('mount-time requests under StrictMode', () => {
  it('VerifyEmailScreen reaches a terminal state, and asks once', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    renderStrict(<VerifyEmailScreen token="TOK" onDone={() => {}} />)

    // Terminal state — not the spinner.
    expect(
      await screen.findByRole('heading', { name: /e-mail confirmed/i }, { timeout: 3000 }),
    ).toBeInTheDocument()
    // And the single-use token was spent exactly once.
    expect(post).toHaveBeenCalledTimes(1)
  })

  it('EnrollTotpScreen shows the QR, and starts one enrollment', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { secret: 'JBSWY3DPEHPK3PXP', qr_url: '/api/auth/2fa/totp/qr.png' },
    } as never)
    renderStrict(<EnrollTotpScreen />)

    expect(await screen.findByAltText(/qr code/i, {}, { timeout: 3000 })).toBeInTheDocument()
    // Starting twice would replace the seed under a user already scanning.
    expect(post).toHaveBeenCalledTimes(1)
  })

  it('VerifyEmailScreen still reports a dead link under StrictMode', async () => {
    vi.spyOn(http, 'post').mockImplementation(() =>
      Promise.reject({ response: { status: 404, data: { error: { code: 'verify_invalid' } } } }) as never,
    )
    renderStrict(<VerifyEmailScreen token="DEAD" onDone={() => {}} />)

    expect(
      await screen.findByRole('heading', { name: /no longer valid/i }, { timeout: 3000 }),
    ).toBeInTheDocument()
  })
})

import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { VerifyEmailScreen } from './VerifyEmailScreen'
import { renderWithProviders } from '../../test/renderWithProviders'
import { http } from '../../api/client'

afterEach(() => vi.restoreAllMocks())

function render(onDone = vi.fn()) {
  renderWithProviders(<VerifyEmailScreen token="TOK" onDone={onDone} />, { session: null })
  return onDone
}

describe('VerifyEmailScreen', () => {
  it('spends the token on mount and reports success', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    render()

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/email/verify', { token: 'TOK' }),
    )
    expect(await screen.findByRole('heading', { name: /e-mail confirmed/i })).toBeInTheDocument()
  })

  /**
   * The trap. The token is single-use, so a second request would spend nothing
   * and answer 404 — turning a successful confirmation into "this link is no
   * longer valid" on screen. React 19's StrictMode double-mounts in
   * development, which is exactly when someone would first see it.
   */
  it('sends the request exactly once', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    render()

    await screen.findByRole('heading', { name: /e-mail confirmed/i })
    await new Promise((r) => setTimeout(r, 30))
    expect(post).toHaveBeenCalledTimes(1)
  })

  // A dead link and an unreachable server need different things from the user —
  // a new e-mail versus a retry — so they must not share copy.
  it('distinguishes a dead link from an unreachable server', async () => {
    vi.spyOn(http, 'post').mockImplementation(() =>
      Promise.reject({
        response: { status: 404, data: { error: { code: 'verify_invalid' } } },
      }) as never,
    )
    render()
    expect(
      await screen.findByRole('heading', { name: /no longer valid/i }),
    ).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(/request a new one/i)
  })

  it('offers a retry when the server cannot be reached', async () => {
    vi.spyOn(http, 'post').mockImplementation(() => Promise.reject({ request: {} }) as never)
    render()
    expect(
      await screen.findByRole('heading', { name: /could not confirm/i }),
    ).toBeInTheDocument()
    // And it must NOT claim the link is dead — it is still spendable.
    expect(screen.getByRole('alert')).toHaveTextContent(/still valid/i)
  })

  it('shows a working state before the request settles', () => {
    vi.spyOn(http, 'post').mockImplementation(() => new Promise(() => {}) as never)
    render()
    expect(screen.getByRole('heading', { name: /confirming your e-mail/i })).toBeInTheDocument()
    expect(screen.getByRole('status')).toBeInTheDocument()
  })

  it('hands control back when the user continues', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    const onDone = render()

    await screen.findByRole('heading', { name: /e-mail confirmed/i })
    await user.click(screen.getByRole('button', { name: /continue/i }))
    expect(onDone).toHaveBeenCalledTimes(1)
  })
})

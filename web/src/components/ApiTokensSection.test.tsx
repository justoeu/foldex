import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiTokensSection } from './ApiTokensSection'
import { renderWithProviders } from '../test/renderWithProviders'
import { http } from '../api/client'

afterEach(() => vi.restoreAllMocks())

function mockList(tokens: unknown[]) {
  return vi.spyOn(http, 'get').mockResolvedValue({ data: { tokens } } as never)
}

describe('ApiTokensSection', () => {
  it('says so when there are no tokens', async () => {
    mockList([])
    renderWithProviders(<ApiTokensSection />)
    expect(await screen.findByText(/no tokens yet/i)).toBeInTheDocument()
  })

  it('lists existing tokens with when they were last used', async () => {
    mockList([
      { id: 1, name: 'extension', scope: 'content', created_at: '2026-01-01T00:00:00Z' },
      {
        id: 2,
        name: 'script',
        scope: 'content',
        created_at: '2026-01-01T00:00:00Z',
        last_used_at: '2026-02-01T00:00:00Z',
      },
    ])
    renderWithProviders(<ApiTokensSection />)

    expect(await screen.findByText('extension')).toBeInTheDocument()
    expect(screen.getByText(/never used/i)).toBeInTheDocument()
    expect(screen.getByText(/last used/i)).toBeInTheDocument()
  })

  it('needs a name before it will create one', async () => {
    const user = userEvent.setup()
    mockList([])
    renderWithProviders(<ApiTokensSection />)

    const create = await screen.findByRole('button', { name: /create token/i })
    expect(create).toBeDisabled()

    await user.type(screen.getByLabelText(/name/i), 'extension')
    expect(create).toBeEnabled()
  })

  /**
   * The plaintext appears in exactly one response and nowhere else — the server
   * keeps only sha256. Showing it prominently, with the warning, is not polish:
   * a user who clicks past it has to revoke and start over.
   */
  it('shows the new token once, with the warning', async () => {
    const user = userEvent.setup()
    mockList([])
    vi.spyOn(http, 'post').mockResolvedValue({
      data: { id: 1, name: 'extension', scope: 'content', created_at: '', token: 'fx_1_secret' },
    } as never)
    renderWithProviders(<ApiTokensSection />)

    await user.type(await screen.findByLabelText(/name/i), 'extension')
    await user.click(screen.getByRole('button', { name: /create token/i }))

    expect(await screen.findByTestId('new-token')).toHaveTextContent('fx_1_secret')
    expect(screen.getByText(/only time it is shown/i)).toBeInTheDocument()
    expect(vi.mocked(http.post)).toHaveBeenCalledWith('/api/auth/tokens', { name: 'extension' })
  })

  it('dismisses the plaintext when the user is done with it', async () => {
    const user = userEvent.setup()
    mockList([])
    vi.spyOn(http, 'post').mockResolvedValue({
      data: { id: 1, name: 'extension', scope: 'content', created_at: '', token: 'fx_1_secret' },
    } as never)
    renderWithProviders(<ApiTokensSection />)

    await user.type(await screen.findByLabelText(/name/i), 'extension')
    await user.click(screen.getByRole('button', { name: /create token/i }))
    await screen.findByTestId('new-token')

    await user.click(screen.getByRole('button', { name: /^done$/i }))
    expect(screen.queryByTestId('new-token')).not.toBeInTheDocument()
  })

  // Revoking breaks whatever is using the token, so it goes through the same
  // destructive confirmation every other irreversible action does.
  it('confirms before revoking', async () => {
    const user = userEvent.setup()
    mockList([{ id: 7, name: 'extension', scope: 'content', created_at: '' }])
    const del = vi.spyOn(http, 'delete').mockResolvedValue({ data: {} } as never)
    renderWithProviders(<ApiTokensSection />)

    await user.click(await screen.findByRole('button', { name: /revoke extension/i }))
    expect(await screen.findByText(/revoke this token/i)).toBeInTheDocument()
    expect(del).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: /^confirm$/i }))
    await waitFor(() => expect(del).toHaveBeenCalledWith('/api/auth/tokens/7'))
  })

  it('explains the cap instead of failing silently', async () => {
    const user = userEvent.setup()
    mockList([])
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'too_many_tokens' } } },
    })
    renderWithProviders(<ApiTokensSection />)

    await user.type(await screen.findByLabelText(/name/i), 'one more')
    await user.click(screen.getByRole('button', { name: /create token/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/revoke an existing token/i)
  })

  // The scope is a real limit, and saying so on screen is what stops someone
  // from treating the token as "my account in a string".
  it('states the empty list until a token exists', async () => {
    mockList([])
    renderWithProviders(<ApiTokensSection />)
    expect(await screen.findByText(/no tokens yet|nenhum token/i)).toBeInTheDocument()
  })

  // ── Rotation ────────────────────────────────────────────────────────

  function mockRotatePair() {
    vi.spyOn(http, 'delete').mockResolvedValue({ data: {} } as never)
    return vi.spyOn(http, 'post').mockResolvedValue({
      data: { id: 8, name: 'extension', scope: 'content', created_at: '', token: 'fx_2_secret' },
    } as never)
  }

  async function rotateThe(user: ReturnType<typeof userEvent.setup>, name: string) {
    await user.click(await screen.findByRole('button', { name: new RegExp(`rotate ${name}`, 'i') }))
    await user.click(screen.getByRole('button', { name: /^confirm$/i }))
  }

  // Rotation is the replace flow the one-token cap implies: the new plaintext
  // must land in the same single-display band creation uses.
  it('shows the rotated token in the secret band', async () => {
    const user = userEvent.setup()
    mockList([{ id: 7, name: 'extension', scope: 'content', created_at: '' }])
    mockRotatePair()
    renderWithProviders(<ApiTokensSection />)

    await rotateThe(user, 'extension')

    expect(await screen.findByTestId('new-token')).toHaveTextContent('fx_2_secret')
    expect(vi.mocked(http.delete)).toHaveBeenCalledWith('/api/auth/tokens/7')
    expect(vi.mocked(http.post)).toHaveBeenCalledWith('/api/auth/tokens', { name: 'extension' })
  })

  // The pair is not a transaction. When the create half fails the honest
  // message is the one that says the old token is GONE and a new one can be
  // created normally — not a generic "something went wrong".
  it('explains the mid-pair failure instead of failing generically', async () => {
    const user = userEvent.setup()
    mockList([{ id: 7, name: 'extension', scope: 'content', created_at: '' }])
    vi.spyOn(http, 'delete').mockResolvedValue({ data: {} } as never)
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 500, data: { error: { code: 'internal' } } },
    })
    renderWithProviders(<ApiTokensSection />)

    await rotateThe(user, 'extension')

    expect(await screen.findByRole('alert')).toHaveTextContent(/old token was revoked/i)
    expect(screen.getByRole('alert')).toHaveTextContent(/create a token/i)
    // The revoke half already landed, so the list must refetch — a stale row
    // would render the revoked token as live and rotate-a-ghost would 404.
    expect(vi.mocked(http.get).mock.calls.filter((c) => c[0] === '/api/auth/tokens').length).toBeGreaterThan(1)
  })

  it('answers a 409 from the create half with the cap copy', async () => {
    const user = userEvent.setup()
    mockList([{ id: 7, name: 'extension', scope: 'content', created_at: '' }])
    vi.spyOn(http, 'delete').mockResolvedValue({ data: {} } as never)
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'too_many_tokens' } } },
    })
    renderWithProviders(<ApiTokensSection />)

    await rotateThe(user, 'extension')

    expect(await screen.findByRole('alert')).toHaveTextContent(/revoke an existing token/i)
  })

  it('disables the rotate button while the pair is in flight', async () => {
    const user = userEvent.setup()
    mockList([{ id: 7, name: 'extension', scope: 'content', created_at: '' }])
    let releaseRevoke: (() => void) | undefined
    vi.spyOn(http, 'delete').mockImplementation(
      () => new Promise((res) => { releaseRevoke = () => res({ data: {} } as never) }),
    )
    vi.spyOn(http, 'post').mockResolvedValue({
      data: { id: 8, name: 'extension', scope: 'content', created_at: '', token: 'fx_2_secret' },
    } as never)
    renderWithProviders(<ApiTokensSection />)

    const btn = await screen.findByRole('button', { name: /rotate extension/i })
    await user.click(btn)
    await user.click(screen.getByRole('button', { name: /^confirm$/i }))

    expect(screen.getByRole('button', { name: /rotate extension/i })).toBeDisabled()

    releaseRevoke?.()
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /rotate extension/i })).toBeEnabled(),
    )
  })
})

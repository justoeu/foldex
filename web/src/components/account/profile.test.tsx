import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AccountPage } from '../../pages/AccountPage'
import { renderWithProviders, testAdminSession } from '../../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../../test/server'
import { http } from '../../api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('account page — profile', () => {
  it('shows the signed-in identity and saves a renamed display name', async () => {
    const session = testAdminSession as { user: object; features: object }
    const patch = vi.spyOn(http, 'patch').mockResolvedValue({ data: {
      status: 'authenticated',
      user: { ...session.user, name: 'Valmir Justo' },
      csrfToken: 'test-csrf-token',
      features: session.features,
    } } as never)

    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()

    // The old version of this assertion matched /administrator/i and passed on
    // the NAME FIELD'S HINT ("shown to administrators") — it never touched the
    // role. Asserted against the identity block itself now.
    expect(screen.getByRole('heading', { name: 'Test Admin' })).toBeInTheDocument()
    expect(screen.getByText('admin@foldex.test')).toBeInTheDocument()
    expect(screen.getByText('Admin')).toBeInTheDocument() // the role chip

    const field = screen.getByLabelText(/display name/i)
    await user.clear(field)
    await user.type(field, 'Valmir Justo')
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() => expect(patch).toHaveBeenCalledWith('/api/auth/profile', { name: 'Valmir Justo' }))
    patch.mockRestore()
  })

  it('sends the language only when it changed, so a rename cannot undo it', async () => {
    const session = testAdminSession as { user: object; features: object }
    const patch = vi.spyOn(http, 'patch').mockResolvedValue({ data: {
      status: 'authenticated',
      user: { ...session.user, name: 'Valmir Justo' },
      csrfToken: 'test-csrf-token',
      features: session.features,
    } } as never)

    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()

    const field = screen.getByLabelText(/display name/i)
    await user.clear(field)
    await user.type(field, 'Valmir Justo')
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    // The language field was never touched, so it must not travel. Sending it
    // would overwrite whatever is stored — including a value changed from
    // another tab since this screen loaded.
    await waitFor(() => expect(patch).toHaveBeenCalledWith('/api/auth/profile', { name: 'Valmir Justo' }))
    patch.mockRestore()
  })

  it('saves a chosen language alongside the name', async () => {
    const session = testAdminSession as { user: object; features: object }
    const patch = vi.spyOn(http, 'patch').mockResolvedValue({ data: {
      status: 'authenticated',
      user: { ...session.user, locale: 'pt' },
      csrfToken: 'test-csrf-token',
      features: session.features,
    } } as never)

    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()

    await user.selectOptions(screen.getByLabelText(/language/i), 'pt')
    await user.click(screen.getByRole('button', { name: /save changes/i }))
    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/auth/profile', { name: 'Test Admin', locale: 'pt' }))
    patch.mockRestore()
  })

  it('offers following the browser as a real choice, not only the languages', () => {
    renderWithProviders(<AccountPage />)
    const select = screen.getByLabelText(/language/i)

    // Without an explicit "follow my browser" entry, a user who picked a
    // language once would have no way back to the default.
    const options = Array.from(select.querySelectorAll('option')).map((o) => o.value)
    expect(options).toContain('')
    expect(options).toEqual(expect.arrayContaining(['en', 'pt', 'es']))
  })

  it('keeps save disabled until the name changes (no spurious API calls)', async () => {
    const patch = vi.spyOn(http, 'patch')
    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()

    const save = screen.getByRole('button', { name: /save changes/i })
    expect(save).toBeDisabled()
    await user.click(save)
    expect(patch).not.toHaveBeenCalled()
    patch.mockRestore()
  })

  // maxLength clamps typing, so the client-side guard is reached by setting
  // the value programmatically (fireEvent bypasses the clamp).
  it('blocks an over-long name client-side without hitting the API', async () => {
    const patch = vi.spyOn(http, 'patch')
    renderWithProviders(<AccountPage />)

    fireEvent.change(screen.getByLabelText(/display name/i), { target: { value: 'x'.repeat(121) } })
    await userEvent.setup().click(screen.getByRole('button', { name: /save changes/i }))

    expect(await screen.findByText(/at most 120 characters/i)).toBeInTheDocument()
    expect(patch).not.toHaveBeenCalled()
    patch.mockRestore()
  })

  // The previous version waited for `/your profile/i` to disappear — a string
  // that exists nowhere in the app, so the wait resolved on the first tick
  // whether or not anything signed out. Asserted on the request now.
  // The session actions live behind their own rail item.
  it('signs out via the session action', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({ status: 204 } as never)
    renderWithProviders(<AccountPage initialTab="sessions" />)

    await userEvent.setup().click(screen.getByRole('button', { name: /^sign out$/i }))

    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/auth/logout'))
    // AuthGate takes over on the anonymous session, so the page itself goes.
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: /^sign out$/i })).not.toBeInTheDocument(),
    )
    post.mockRestore()
  })

  // The comment on signOutEverywhere states this as an invariant: the local
  // session is being abandoned either way, and staying signed in here after
  // asking to be signed out everywhere is the one outcome nobody asked for.
  it('still signs out locally when logout-all fails', async () => {
    const post = vi.spyOn(http, 'post').mockImplementation(async (url: string) => {
      if (url === '/api/auth/logout-all') throw new Error('network')
      return { status: 204 } as never
    })
    renderWithProviders(<AccountPage initialTab="sessions" />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /sign out everywhere/i }))
    await user.click(await screen.findByRole('button', { name: /^confirm$/i }))

    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/auth/logout'))
    post.mockRestore()
  })

  // The remembered address must not outlive the gesture that ends every session.
  it('forgets the remembered e-mail on sign out everywhere', async () => {
    localStorage.setItem('foldex.auth.email', 'saved@foldex.test')
    vi.spyOn(http, 'post').mockResolvedValue({ status: 204 } as never)
    renderWithProviders(<AccountPage initialTab="sessions" />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /sign out everywhere/i }))
    await user.click(await screen.findByRole('button', { name: /^confirm$/i }))

    await waitFor(() => expect(localStorage.getItem('foldex.auth.email')).toBeNull())
  })

  it('signs out everywhere only after the destructive confirmation', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({ status: 204 } as never)
    renderWithProviders(<AccountPage initialTab="sessions" />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /sign out everywhere/i }))
    // Confirmation dialog first — this action kills every other device.
    const confirmBtn = await screen.findByRole('button', { name: /^confirm$/i })
    await user.click(confirmBtn)

    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/auth/logout-all'))
    // ...and the local session followed (the page unmounts on anonymous).
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/auth/logout'))
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: /sign out everywhere/i })).not.toBeInTheDocument(),
    )
    post.mockRestore()
  })

  it('does nothing when the logout-all confirmation is cancelled', async () => {
    const post = vi.spyOn(http, 'post')
    renderWithProviders(<AccountPage initialTab="sessions" />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /sign out everywhere/i }))
    await user.click(await screen.findByRole('button', { name: /^cancel$/i }))

    expect(post).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /sign out everywhere/i })).toBeInTheDocument()
    post.mockRestore()
  })

  it('surfaces a server error without losing the form state', async () => {
    vi.spyOn(http, 'patch').mockRejectedValue({
      response: { status: 400, data: { error: { code: 'invalid_name' } } },
    } as never)
    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()

    await user.clear(screen.getByLabelText(/display name/i))
    await user.type(screen.getByLabelText(/display name/i), 'x'.repeat(121))
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    expect(await screen.findByText(/at most 120 characters/i)).toBeInTheDocument()
    // The title of this test promised the form state survived and the body
    // never looked. `PasswordCard` clears its code field on error for a real
    // reason, and copying that habit here would empty a 120-character name the
    // user must now retype — silently, with the suite still green.
    expect(screen.getByLabelText(/display name/i)).toHaveValue('x'.repeat(120))
    vi.restoreAllMocks()
  })
})

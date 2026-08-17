import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ProfileSection, initialsOf } from './ProfileSection'
import { renderWithProviders, testAdminSession } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('initialsOf', () => {
  it('derives initials from first and last words, falling back to e-mail', () => {
    expect(initialsOf('Valmir Justo', 'v@x.test')).toBe('VJ')
    expect(initialsOf('grace', 'g@x.test')).toBe('G')
    expect(initialsOf('', 'grace@x.test')).toBe('G')
    expect(initialsOf('', '')).toBe('?')
    expect(initialsOf('  leading spaces  here ', 'x@x.test')).toBe('LH')
  })
})

describe('ProfileSection', () => {
  it('shows the signed-in identity and saves a renamed display name', async () => {
    const session = testAdminSession as { user: object; features: object }
    const patch = vi.spyOn(http, 'patch').mockResolvedValue({ data: {
      status: 'authenticated',
      user: { ...session.user, name: 'Valmir Justo' },
      csrfToken: 'test-csrf-token',
      features: session.features,
    } } as never)

    renderWithProviders(<ProfileSection />)
    const user = userEvent.setup()

    expect(screen.getByText('Test Admin')).toBeInTheDocument()
    expect(screen.getAllByText(/admin@foldex\.test/i).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/administrator/i).length).toBeGreaterThan(0)

    const field = screen.getByLabelText(/display name/i)
    await user.clear(field)
    await user.type(field, 'Valmir Justo')
    await user.click(screen.getByRole('button', { name: /save name/i }))

    await waitFor(() => expect(patch).toHaveBeenCalledWith('/api/auth/profile', { name: 'Valmir Justo' }))
    patch.mockRestore()
  })

  it('keeps save disabled until the name changes (no spurious API calls)', async () => {
    const patch = vi.spyOn(http, 'patch')
    renderWithProviders(<ProfileSection />)
    const user = userEvent.setup()

    const save = screen.getByRole('button', { name: /save name/i })
    expect(save).toBeDisabled()
    await user.click(save)
    expect(patch).not.toHaveBeenCalled()
    patch.mockRestore()
  })

  // maxLength clamps typing, so the client-side guard is reached by setting
  // the value programmatically (fireEvent bypasses the clamp).
  it('blocks an over-long name client-side without hitting the API', async () => {
    const patch = vi.spyOn(http, 'patch')
    renderWithProviders(<ProfileSection />)

    fireEvent.change(screen.getByLabelText(/display name/i), { target: { value: 'x'.repeat(121) } })
    await userEvent.setup().click(screen.getByRole('button', { name: /save name/i }))

    expect(await screen.findByText(/at most 120 characters/i)).toBeInTheDocument()
    expect(patch).not.toHaveBeenCalled()
    patch.mockRestore()
  })

  it('signs out via the session action', async () => {
    renderWithProviders(<ProfileSection />)
    await userEvent.setup().click(screen.getByRole('button', { name: /^sign out$/i }))
    // AuthGate takes over on the anonymous session; all we assert here is
    // that the button drove signOut (the POST is mocked by the axios layer).
    await waitFor(() => expect(screen.queryByText(/your profile/i)).not.toBeInTheDocument())
  })

  it('signs out everywhere only after the destructive confirmation', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({ status: 204 } as never)
    renderWithProviders(<ProfileSection />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /sign out everywhere/i }))
    // Confirmation dialog first — this action kills every other device.
    const confirmBtn = await screen.findByRole('button', { name: /^confirm$/i })
    await user.click(confirmBtn)

    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/auth/logout-all'))
    // ...and the local session followed (profile unmounts on anonymous).
    await waitFor(() => expect(screen.queryByRole('button', { name: /sign out everywhere/i })).not.toBeInTheDocument())
    post.mockRestore()
  })

  it('does nothing when the logout-all confirmation is cancelled', async () => {
    const post = vi.spyOn(http, 'post')
    renderWithProviders(<ProfileSection />)
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
    renderWithProviders(<ProfileSection />)
    const user = userEvent.setup()

    await user.clear(screen.getByLabelText(/display name/i))
    await user.type(screen.getByLabelText(/display name/i), 'x'.repeat(121))
    await user.click(screen.getByRole('button', { name: /save name/i }))

    expect(await screen.findByText(/at most 120 characters/i)).toBeInTheDocument()
    vi.restoreAllMocks()
  })
})

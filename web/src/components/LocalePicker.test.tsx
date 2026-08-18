import { describe, it, expect, afterEach, vi } from 'vitest'
import { screen, within, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LocalePicker } from './LocalePicker'
import { renderWithProviders, testAdminSession } from '../test/renderWithProviders'
import { http } from '../api/client'
import i18n from '../i18n'

const langName = /language|idioma/i

afterEach(async () => {
  vi.restoreAllMocks()
  // Reset shared i18n state so locale changes don't leak across tests.
  if (i18n.language !== 'en') await i18n.changeLanguage('en')
})

describe('LocalePicker', () => {
  it('shows the active locale code on the button', () => {
    renderWithProviders(<LocalePicker />)
    expect(screen.getByRole('button', { name: langName })).toHaveTextContent('en')
  })

  it('opens the menu on click and lists every supported locale', async () => {
    const user = userEvent.setup()
    renderWithProviders(<LocalePicker />)
    expect(screen.queryByRole('menu')).toBeNull()

    await user.click(screen.getByRole('button', { name: langName }))

    const menu = screen.getByRole('menu')
    expect(within(menu).getByRole('menuitem', { name: /English/ })).toBeInTheDocument()
    expect(within(menu).getByRole('menuitem', { name: /Português/ })).toBeInTheDocument()
    expect(within(menu).getByRole('menuitem', { name: /Español/ })).toBeInTheDocument()
  })

  it('marks the current locale with aria-current', async () => {
    const user = userEvent.setup()
    renderWithProviders(<LocalePicker />)
    await user.click(screen.getByRole('button', { name: langName }))

    expect(screen.getByRole('menuitem', { name: /English/ })).toHaveAttribute('aria-current', 'true')
    expect(screen.getByRole('menuitem', { name: /Português/ })).not.toHaveAttribute('aria-current')
  })

  it('renders the menu through a portal on <body> (escapes the topbar clip)', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<LocalePicker />)
    await user.click(screen.getByRole('button', { name: langName }))

    const menu = screen.getByRole('menu')
    // The regression: the topbar's `overflow: hidden` would clip an in-tree
    // dropdown, so the menu must be portaled out of the component's subtree.
    expect(container.contains(menu)).toBe(false)
    expect(document.body.contains(menu)).toBe(true)
  })

  it('picking a locale switches the language and closes the menu', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(i18n, 'changeLanguage')
    renderWithProviders(<LocalePicker />)

    await user.click(screen.getByRole('button', { name: langName }))
    await user.click(screen.getByRole('menuitem', { name: /Português/ }))

    expect(spy).toHaveBeenCalledWith('pt')
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('keeps the menu open on mousedown over an option (outside-click guard)', async () => {
    const user = userEvent.setup()
    renderWithProviders(<LocalePicker />)
    await user.click(screen.getByRole('button', { name: langName }))

    // Isolate the bug directly: a bare mousedown on an option must NOT close
    // the menu (the guard whitelists the portaled subtree). If it did, the
    // option's subsequent click would land on a dead node.
    fireEvent.mouseDown(screen.getByRole('menuitem', { name: /Español/ }))
    expect(screen.getByRole('menu')).toBeInTheDocument()
  })

  it('closes when clicking outside the menu', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <>
        <LocalePicker />
        <button data-testid="outside">outside</button>
      </>,
    )
    await user.click(screen.getByRole('button', { name: langName }))
    expect(screen.getByRole('menu')).toBeInTheDocument()

    await user.click(screen.getByTestId('outside'))
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('toggles closed when the button is clicked again', async () => {
    const user = userEvent.setup()
    renderWithProviders(<LocalePicker />)
    const btn = screen.getByRole('button', { name: langName })

    await user.click(btn)
    expect(screen.getByRole('menu')).toBeInTheDocument()
    await user.click(btn)
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('closes on Escape without changing the language', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(i18n, 'changeLanguage')
    renderWithProviders(<LocalePicker />)

    await user.click(screen.getByRole('button', { name: langName }))
    expect(screen.getByRole('menu')).toBeInTheDocument()

    await user.keyboard('{Escape}')

    expect(screen.queryByRole('menu')).toBeNull()
    expect(spy).not.toHaveBeenCalled()
  })

  // The picker and the profile field are ONE setting. Without the write-through
  // the account keeps its old preference, and useAccountLocale re-applies it on
  // the next load — silently undoing a choice the user watched take effect.
  it('carries the pick to the account so the next load does not undo it', async () => {
    const user = userEvent.setup()
    const patch = vi.spyOn(http, 'patch').mockResolvedValue({ data: {
      status: 'authenticated',
      user: { ...(testAdminSession as { user: object }).user, locale: 'pt' },
      csrf_token: 'test-csrf-token',
      features: (testAdminSession as { features: object }).features,
    } } as never)

    renderWithProviders(<LocalePicker />)
    await user.click(screen.getByRole('button', { name: langName }))
    await user.click(screen.getByRole('menuitem', { name: /Português/ }))

    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/auth/profile', { name: 'Test Admin', locale: 'pt' }))
  })

  it('does not write when the account already holds that language', async () => {
    const user = userEvent.setup()
    const patch = vi.spyOn(http, 'patch')
    const session = testAdminSession as { user: Record<string, unknown> }
    renderWithProviders(<LocalePicker />, {
      session: { ...testAdminSession, user: { ...session.user, locale: 'pt' } } as never,
    })

    await user.click(screen.getByRole('button', { name: langName }))
    await user.click(screen.getByRole('menuitem', { name: /Português/ }))

    expect(patch).not.toHaveBeenCalled()
  })

  // A failed write must revert NOW. Leaving it would show the new language
  // until some later mount reverted it from the server value — a change the
  // user could not connect to anything they did.
  it('reverts the language when the account write fails', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'patch').mockRejectedValue(new Error('offline') as never)
    renderWithProviders(<LocalePicker />)

    await user.click(screen.getByRole('button', { name: langName }))
    await user.click(screen.getByRole('menuitem', { name: /Português/ }))

    await waitFor(() => expect(i18n.resolvedLanguage).toBe('en'))
    expect(screen.getByRole('button', { name: langName })).toHaveTextContent('en')
  })

  // Auth screens render the picker too, and there is no account to write to.
  it('changes the language with no session and writes nothing', async () => {
    const user = userEvent.setup()
    const patch = vi.spyOn(http, 'patch')
    renderWithProviders(<LocalePicker />, {
      session: { status: 'anonymous', features: (testAdminSession as { features: object }).features } as never,
    })

    await user.click(screen.getByRole('button', { name: langName }))
    await user.click(screen.getByRole('menuitem', { name: /Español/ }))

    await waitFor(() => expect(i18n.resolvedLanguage).toBe('es'))
    expect(patch).not.toHaveBeenCalled()
  })
})

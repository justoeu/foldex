import { describe, it, expect, afterEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AuthShell } from './AuthShell'
import { ForgotScreen } from './ForgotScreen'
import { VerifyEmailScreen } from './VerifyEmailScreen'
import { renderWithProviders, testAdminSession, testAdminUser } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import i18n from '../../i18n'

const features = (testAdminSession as { features: object }).features

const anonymous = { status: 'anonymous', features } as never

/** The shape `adopt` actually consumes — a MeResponse, not a SessionState. */
function meResponse(locale: string) {
  return {
    data: {
      status: 'authenticated',
      user: { ...testAdminUser, locale },
      csrf_token: 'test-csrf-token',
      features,
    },
  } as never
}

afterEach(async () => {
  vi.restoreAllMocks()
  // `await`ed, unlike the global reset in test/setup.ts which fires `void`:
  // these tests assert on `aria-pressed`, which is derived from the resolved
  // language, so a change still in flight leaks into the next test's first
  // render as the wrong active flag.
  if (i18n.language !== 'en') await i18n.changeLanguage('en')
})

describe('AuthLocaleSwitcher', () => {
  it('offers one flag button per supported locale', () => {
    renderWithProviders(<AuthShell title="Sign in">form</AuthShell>, { session: anonymous })

    expect(screen.getByRole('button', { name: 'English' })).toHaveTextContent('EN')
    expect(screen.getByRole('button', { name: 'Português' })).toHaveTextContent('PT')
    expect(screen.getByRole('button', { name: 'Español' })).toHaveTextContent('ES')
  })

  // The flag is decorative: a flag is a country, not a language, and on some
  // platforms the emoji does not draw at all. If the accessible name ever came
  // from the glyph, those users would meet three unlabelled buttons.
  it('names each button by its language, never by the flag glyph', () => {
    renderWithProviders(<AuthShell title="Sign in">form</AuthShell>, { session: anonymous })

    const pt = screen.getByRole('button', { name: 'Português' })
    expect(pt).toHaveAccessibleName('Português')
    expect(pt.textContent).toContain('🇧🇷')
  })

  it('marks the active locale pressed and the others not', () => {
    renderWithProviders(<AuthShell title="Sign in">form</AuthShell>, { session: anonymous })

    expect(screen.getByRole('button', { name: 'English' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Português' })).toHaveAttribute(
      'aria-pressed',
      'false',
    )
  })

  // A regional tag is the realistic input — it is what `navigator.language` and
  // the persisted detector hand i18next — and it is the only case where
  // `resolvedLanguage` and `language` disagree. Reading the wrong one leaves
  // every flag unpressed for a `pt-BR` browser.
  it('marks the base locale pressed for a regional tag like pt-BR', async () => {
    await i18n.changeLanguage('pt-BR')
    renderWithProviders(<AuthShell title="Sign in">form</AuthShell>, { session: anonymous })

    expect(screen.getByRole('button', { name: 'Português' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
  })

  it('switches the interface language on click', async () => {
    const user = userEvent.setup()
    renderWithProviders(<AuthShell title="Sign in">form</AuthShell>, { session: anonymous })

    await user.click(screen.getByRole('button', { name: 'Português' }))

    await waitFor(() => expect(i18n.resolvedLanguage).toBe('pt'))
    expect(screen.getByRole('button', { name: 'Português' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
  })

  // Every screen AuthGate routes to below its authenticated short-circuit is
  // anonymous, and PATCH /api/auth/profile requires a session.
  it('writes nothing to the account when there is no session', async () => {
    const user = userEvent.setup()
    const patch = vi.spyOn(http, 'patch')
    renderWithProviders(<AuthShell title="Sign in">form</AuthShell>, { session: anonymous })

    await user.click(screen.getByRole('button', { name: 'Español' }))

    await waitFor(() => expect(i18n.resolvedLanguage).toBe('es'))
    expect(patch).not.toHaveBeenCalled()
  })

  // A half-finished login is NOT a session: `useCurrentUser` resolves to null
  // for `two_factor_required`, so the pick must reach i18next and stop there.
  // A write from here would be a guaranteed 401 against a live pre-auth
  // challenge the user still has to finish.
  it('writes nothing while a second factor is still pending', async () => {
    const user = userEvent.setup()
    const patch = vi.spyOn(http, 'patch')
    renderWithProviders(<AuthShell title="Two-factor">form</AuthShell>, {
      session: {
        status: 'two_factor_required',
        pending: { purpose: 'login', methods: ['totp'], expires_at: '2099-01-01T00:00:00Z' },
        features,
      } as never,
    })

    await user.click(screen.getByRole('button', { name: 'Español' }))

    await waitFor(() => expect(i18n.resolvedLanguage).toBe('es'))
    expect(patch).not.toHaveBeenCalled()
  })

  // VerifyEmailScreen is the ONE auth screen AuthGate mounts ABOVE its
  // authenticated short-circuit, so it is the only one where the account
  // write-through actually fires. The picker and the profile field are one
  // setting (see useLocaleChoice), and without the write `useAccountLocale`
  // re-applies the old preference on the next load.
  it('carries the pick to the account on the one screen that can hold a session', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    const patch = vi.spyOn(http, 'patch').mockResolvedValue(meResponse('pt'))

    renderWithProviders(<VerifyEmailScreen token="tok" onDone={() => {}} />)

    await user.click(screen.getByRole('button', { name: 'Português' }))

    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/auth/profile', { locale: 'pt' }),
    )
  })

  // Proves `adopt` actually landed: the second click can only be a no-op if the
  // session in context now reports `locale: 'pt'`. Asserting the first PATCH
  // alone would pass with `adopt` wired to nothing.
  it('adopts the returned session, so re-picking the same language writes nothing', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    const patch = vi.spyOn(http, 'patch').mockResolvedValue(meResponse('pt'))

    renderWithProviders(<VerifyEmailScreen token="tok" onDone={() => {}} />)

    await user.click(screen.getByRole('button', { name: 'Português' }))
    await waitFor(() => expect(patch).toHaveBeenCalledTimes(1))

    await user.click(screen.getByRole('button', { name: 'Português' }))
    expect(patch).toHaveBeenCalledTimes(1)
  })

  // The dropdown closed on pick; this row stays clickable, so trying all three
  // inside one round-trip is an ordinary gesture. Fired concurrently the
  // responses settle in any order and the account keeps whichever LANDED last
  // rather than whichever was CLICKED last — then useAccountLocale imposes it
  // on the next load, which is the exact failure the write-through prevents.
  it('serializes account writes so the last click wins, not the last response', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)

    const settle: Array<() => void> = []
    const patch = vi
      .spyOn(http, 'patch')
      .mockImplementation(
        (_url: string, body?: unknown) =>
          new Promise((resolve) =>
            settle.push(() => resolve(meResponse((body as { locale: string }).locale))),
          ) as never,
      )

    renderWithProviders(<VerifyEmailScreen token="tok" onDone={() => {}} />)

    await user.click(screen.getByRole('button', { name: 'Português' }))
    await waitFor(() => expect(patch).toHaveBeenCalledTimes(1))

    // Second pick while the first is still in flight: no concurrent request.
    await user.click(screen.getByRole('button', { name: 'Español' }))
    expect(patch).toHaveBeenCalledTimes(1)
    expect(patch).toHaveBeenLastCalledWith('/api/auth/profile', { locale: 'pt' })

    settle[0]()
    await waitFor(() => expect(patch).toHaveBeenCalledTimes(2))
    expect(patch).toHaveBeenLastCalledWith('/api/auth/profile', { locale: 'es' })

    settle[1]()
    await waitFor(() => expect(patch).toHaveBeenCalledTimes(2))
  })

  // A failed write must revert NOW. Leaving it would show the new language
  // until some later mount reverted it from the server value — a change the
  // user could not connect to anything they did.
  it('reverts the pressed flag when the account write fails', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    vi.spyOn(http, 'patch').mockRejectedValue(new Error('offline') as never)

    renderWithProviders(<VerifyEmailScreen token="tok" onDone={() => {}} />)

    await user.click(screen.getByRole('button', { name: 'Português' }))

    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'English' })).toHaveAttribute(
        'aria-pressed',
        'true',
      ),
    )
    expect(screen.getByRole('button', { name: 'Português' })).toHaveAttribute(
      'aria-pressed',
      'false',
    )
  })

  // The switcher is mounted on the shared frame precisely so no screen can be
  // missed. Naming today's screens would only assert what today already does —
  // this holds for screen ten, which is the actual invariant.
  it('every auth screen renders through AuthShell, so none can miss the switcher', () => {
    // The glob is what makes this hold for screen ten: a new *Screen.tsx is
    // picked up without anyone adding it here.
    const screens = import.meta.glob('./*Screen.tsx', {
      query: '?raw',
      import: 'default',
      eager: true,
    }) as Record<string, string>

    expect(Object.keys(screens).length).toBeGreaterThan(5)
    for (const [file, src] of Object.entries(screens)) {
      expect(src, `${file} does not render <AuthShell>`).toMatch(/<AuthShell[\s>]/)
    }
  })

  // The locale the screen is showing travels with the reset request (CLAUDE.md
  // §4), so a user who switches here must get the e-mail in the language they
  // picked — not the one Accept-Language happens to carry.
  it('the language picked here is the one the reset e-mail is asked for', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    renderWithProviders(<ForgotScreen onBack={() => {}} />, { session: anonymous })

    // Captured BEFORE the switch: querying by label afterwards would have to
    // hedge across two languages, and would silently start matching a second
    // button the day this screen grows one.
    const submit = screen.getByRole('button', { name: i18n.t('auth_forgot.submit') })

    await user.click(screen.getByRole('button', { name: 'Português' }))
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('pt'))

    await user.type(screen.getByLabelText(/e-?mail/i), 'someone@foldex.test')
    await user.click(submit)

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/password/forgot', {
        email: 'someone@foldex.test',
        locale: 'pt',
      }),
    )
  })
})

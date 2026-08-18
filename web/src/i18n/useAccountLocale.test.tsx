import { describe, it, expect, afterEach, vi } from 'vitest'
import { renderWithProviders, testAdminSession } from '../test/renderWithProviders'
import { useAccountLocale } from './useAccountLocale'
import i18n from '../i18n'

function Probe() {
  useAccountLocale()
  return <span data-testid="lang">{i18n.resolvedLanguage}</span>
}

const withLocale = (locale?: string) => {
  const session = testAdminSession as unknown as { user: Record<string, unknown> }
  return { ...testAdminSession, user: { ...session.user, locale } } as never
}

afterEach(async () => {
  vi.restoreAllMocks()
  if (i18n.language !== 'en') await i18n.changeLanguage('en')
})

describe('useAccountLocale', () => {
  it('applies the account preference on load', async () => {
    const spy = vi.spyOn(i18n, 'changeLanguage')
    renderWithProviders(<Probe />, { session: withLocale('pt') })

    expect(spy).toHaveBeenCalledWith('pt')
  })

  // NULL means "no preference", not English. An account that never chose keeps
  // following the browser — forcing a default here would replace a reasonable
  // guess with a fixed one, and would fight the device-level picker forever.
  it('leaves the language alone when there is no preference', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage')

    renderWithProviders(<Probe />, { session: withLocale(undefined) })
    expect(spy).not.toHaveBeenCalled()

    renderWithProviders(<Probe />, { session: withLocale('') })
    expect(spy).not.toHaveBeenCalled()
  })

  it('does not re-apply a preference the interface already follows', async () => {
    await i18n.changeLanguage('pt')
    const spy = vi.spyOn(i18n, 'changeLanguage')

    renderWithProviders(<Probe />, { session: withLocale('pt') })

    // Re-applying would be a needless remount of every translated subtree, and
    // under StrictMode's double effect it would run twice per load.
    expect(spy).not.toHaveBeenCalled()
  })

  // Anonymous and setup_required sessions have no account to read, and the hook
  // must not assume one exists.
  it('is inert with no signed-in account', () => {
    const spy = vi.spyOn(i18n, 'changeLanguage')
    renderWithProviders(<Probe />, {
      session: { status: 'anonymous', features: (testAdminSession as { features: object }).features } as never,
    })
    expect(spy).not.toHaveBeenCalled()
  })
})

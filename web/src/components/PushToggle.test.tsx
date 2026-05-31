import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import { PushToggle } from './PushToggle'
import { renderWithProviders } from '../test/renderWithProviders'
import * as webPushHooks from '../hooks/useWebPush'

// PushToggle has four branches — each maps to a distinct accessible label
// and a disabled / pressed state. We mock useWebPush directly because the
// production path requires a live ServiceWorker (jsdom doesn't ship one
// reliably) AND we want each branch isolated for clarity.

beforeEach(() => {
  vi.restoreAllMocks()
})

function mockStatus(
  data: { supported: boolean; permission?: NotificationPermission; subscribed?: boolean } | undefined,
) {
  vi.spyOn(webPushHooks, 'useWebPush').mockReturnValue({
    data,
  } as unknown as ReturnType<typeof webPushHooks.useWebPush>)
}

function mockMutations(opts: { isPending?: boolean } = {}) {
  // Both mutation hooks return shapes useMutation provides. PushToggle only
  // reads .isPending and calls .mutate, so we mock the minimum.
  const subscribe = { mutate: vi.fn(), isPending: opts.isPending ?? false }
  const unsubscribe = { mutate: vi.fn(), isPending: opts.isPending ?? false }
  vi.spyOn(webPushHooks, 'useSubscribePush').mockReturnValue(
    subscribe as unknown as ReturnType<typeof webPushHooks.useSubscribePush>,
  )
  vi.spyOn(webPushHooks, 'useUnsubscribePush').mockReturnValue(
    unsubscribe as unknown as ReturnType<typeof webPushHooks.useUnsubscribePush>,
  )
  return { subscribe, unsubscribe }
}

describe('PushToggle', () => {
  it('renders inert placeholder while status is still resolving', () => {
    mockStatus(undefined)
    mockMutations()
    renderWithProviders(<PushToggle />)
    const btn = screen.getByRole('button', { name: /enable notifications/i })
    expect(btn).toBeDisabled()
    expect(btn).toHaveAttribute('aria-busy', 'true')
  })

  it('renders the unsupported state when the browser has no PushManager', () => {
    mockStatus({ supported: false })
    mockMutations()
    renderWithProviders(<PushToggle />)
    const btn = screen.getByRole('button', { name: /doesn't support push notifications/i })
    expect(btn).toBeDisabled()
  })

  it('renders the denied state when permission was rejected', () => {
    mockStatus({ supported: true, permission: 'denied', subscribed: false })
    mockMutations()
    renderWithProviders(<PushToggle />)
    const btn = screen.getByRole('button', { name: /permission was denied/i })
    expect(btn).toBeDisabled()
  })

  it('renders the subscribe state (not pressed) when permission is default + not subscribed', () => {
    mockStatus({ supported: true, permission: 'default', subscribed: false })
    const { subscribe } = mockMutations()
    renderWithProviders(<PushToggle />)
    const btn = screen.getByRole('button', { name: /enable notifications/i })
    expect(btn).not.toBeDisabled()
    expect(btn).toHaveAttribute('aria-pressed', 'false')
    btn.click()
    expect(subscribe.mutate).toHaveBeenCalledTimes(1)
  })

  it('renders the subscribed state (pressed) and click unsubscribes', () => {
    mockStatus({ supported: true, permission: 'granted', subscribed: true })
    const { unsubscribe } = mockMutations()
    renderWithProviders(<PushToggle />)
    const btn = screen.getByRole('button', { name: /disable notifications/i })
    expect(btn).not.toBeDisabled()
    expect(btn).toHaveAttribute('aria-pressed', 'true')
    btn.click()
    expect(unsubscribe.mutate).toHaveBeenCalledTimes(1)
  })

  it('disables the button while a mutation is pending', () => {
    mockStatus({ supported: true, permission: 'default', subscribed: false })
    mockMutations({ isPending: true })
    renderWithProviders(<PushToggle />)
    expect(screen.getByRole('button', { name: /enable notifications/i })).toBeDisabled()
  })
})

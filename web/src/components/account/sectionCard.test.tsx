import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Notice, SectionCard, SectionRow } from './SectionCard'
import { SessionsSection } from './SessionsSection'
import { renderWithProviders } from '../../test/renderWithProviders'
import { freshState, installAxiosMock } from '../../test/server'
import { http } from '../../api/client'
import { I } from '../icons'

describe('the shared account card', () => {
  // A refused credential and a saved change were 12px one-liners at the same
  // weight as a field hint. The live regions are what make them reach a screen
  // reader at all, and they are per TONE: `info` explains something that was
  // always on screen, so announcing it would interrupt for no event.
  it('announces a failure as an alert and a success as a status', () => {
    renderWithProviders(
      <>
        <Notice tone="bad">that went wrong</Notice>
        <Notice tone="ok">that worked</Notice>
        <Notice tone="info">this is how it works</Notice>
      </>,
    )

    expect(screen.getByRole('alert')).toHaveTextContent('that went wrong')
    expect(screen.getByRole('status')).toHaveTextContent('that worked')
    expect(screen.getByText('this is how it works').closest('p')).not.toHaveAttribute('role')
  })

  // The rail says where you can go; the panel has to say where you landed.
  it('titles the card with a heading', () => {
    renderWithProviders(
      <SectionCard icon={I.user} title="Your profile" subtitle="what it is">
        <p>body</p>
      </SectionCard>,
    )
    expect(screen.getByRole('heading', { name: 'Your profile' })).toBeInTheDocument()
    expect(screen.getByText('what it is')).toBeInTheDocument()
  })

  // Scoping is the point of the group: without it a test — and a screen reader
  // — cannot tell which row a "Turn off" button belongs to.
  it('labels each row so its own controls can be found by name', () => {
    renderWithProviders(
      <div>
        <SectionRow icon={I.lock} name="Password" action={<button>Change</button>} />
        <SectionRow icon={I.globe} name="Google" action={<button>Connect</button>} />
      </div>,
    )

    const row = screen.getByRole('group', { name: 'Password' })
    expect(within(row).getByRole('button', { name: 'Change' })).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Connect' })).not.toBeInTheDocument()
  })

  // `lock` is the reason an expected action is absent; `note` is information.
  // Both used to be grey prose at the foot of the card, where neither said
  // which row it was about.
  it('renders the lock reason inside the row it applies to', () => {
    renderWithProviders(
      <div>
        <SectionRow icon={I.key} name="Authenticator" lock="Administrators cannot turn this off." />
        <SectionRow icon={I.mail} name="E-mail" note="Not available here." />
      </div>,
    )

    const app = screen.getByRole('group', { name: 'Authenticator' })
    expect(within(app).getByText(/administrators cannot/i)).toBeInTheDocument()
    expect(within(app).queryByText(/not available here/i)).not.toBeInTheDocument()
  })
})

describe('the sessions panel', () => {
  beforeEach(() => {
    installAxiosMock(freshState())
  })

  it('tells the two sign-outs apart by what each one ends', async () => {
    renderWithProviders(<SessionsSection onSignOut={() => {}} onSignOutEverywhere={() => {}} />)

    // The distinction the panel exists to make: the current row names THIS
    // device, and the bottom card says what "everywhere" ends. Two buttons
    // whose labels differ by one word is not enough for an action that ends
    // every device.
    expect(await screen.findByText(/this device/i)).toBeInTheDocument()
    expect(screen.getByText(/every device|all sessions/i)).toBeInTheDocument()
  })

  // "Sign out everywhere" reads like it covers everything, and an extension
  // that keeps working afterwards is otherwise a surprise.
  it('says that API tokens survive both', () => {
    renderWithProviders(<SessionsSection onSignOut={() => {}} onSignOutEverywhere={() => {}} />)
    expect(screen.getByText(/not sessions/i)).toBeInTheDocument()
  })

  it('calls the handler each row belongs to', async () => {
    const one = vi.fn()
    const all = vi.fn()
    renderWithProviders(<SessionsSection onSignOut={one} onSignOutEverywhere={all} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /^sign out$/i }))
    expect(one).toHaveBeenCalledTimes(1)
    expect(all).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: /sign out everywhere/i }))
    expect(all).toHaveBeenCalledTimes(1)
  })
})

describe('the sessions device list', () => {
  beforeEach(() => {
    const state = freshState()
    state.sessions = [
      { id: 1, created_at: '2026-09-01T10:00:00Z', last_seen_at: '2026-09-24T12:00:00Z', user_agent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36', ip: '203.0.113.10', current: true },
      { id: 2, created_at: '2026-09-20T10:00:00Z', last_seen_at: '2026-09-22T09:00:00Z', user_agent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 Safari/604.1', ip: '198.51.100.7', current: false },
    ]
    installAxiosMock(state)
  })

  // The device list is the point of the redesign: a person tells their
  // machines apart by browser+OS words, and the row they do not recognize is
  // the one they came here to end.
  it('names devices from the user agent and marks the current one', async () => {
    renderWithProviders(<SessionsSection onSignOut={() => {}} onSignOutEverywhere={() => {}} />)
    expect(await screen.findByText(/chrome · macos/i)).toBeInTheDocument()
    expect(screen.getByText(/safari · ios/i)).toBeInTheDocument()
    expect(screen.getByText(/this device/i)).toBeInTheDocument()
    expect(screen.getAllByText(/203\.0\.113\.10|198\.51\.100\.7/)).toHaveLength(2)
  })

  it('ends one foreign session after a confirmation', async () => {
    const del = vi.spyOn(http, 'delete').mockResolvedValue({ status: 204 } as never)
    renderWithProviders(<SessionsSection onSignOut={() => {}} onSignOutEverywhere={() => {}} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /^end$/i }))
    await user.click(screen.getByRole('button', { name: /^confirm$/i }))

    await waitFor(() => expect(del).toHaveBeenCalledWith('/api/auth/sessions/2'))
    del.mockRestore()
  })

  // The current session's row is a sign-out, not a revoke: the local session
  // is the one being abandoned, and the row routes to the auth flow.
  it('keeps the current row as a plain sign-out', async () => {
    const one = vi.fn()
    renderWithProviders(<SessionsSection onSignOut={one} onSignOutEverywhere={() => {}} />)
    await userEvent.setup().click(await screen.findByRole('button', { name: /^sign out$/i }))
    expect(one).toHaveBeenCalledTimes(1)
  })
})

describe('the sessions failure paths', () => {
  it('surfaces a failed revoke instead of failing silently', async () => {
    const state = freshState()
    state.sessions = [
      { id: 1, created_at: '2026-09-01T10:00:00Z', last_seen_at: '2026-09-24T12:00:00Z', user_agent: 'Mozilla/5.0 (Macintosh) Chrome/140.0.0.0', ip: '203.0.113.10', current: true },
      { id: 2, created_at: '2026-09-20T10:00:00Z', last_seen_at: '2026-09-22T09:00:00Z', user_agent: 'Mozilla/5.0 (iPhone) Safari/604.1', ip: '198.51.100.7', current: false },
    ]
    installAxiosMock(state)
    const del = vi.spyOn(http, 'delete').mockRejectedValue(new Error('network'))
    renderWithProviders(<SessionsSection onSignOut={() => {}} onSignOutEverywhere={() => {}} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /^end$/i }))
    await user.click(screen.getByRole('button', { name: /^confirm$/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/could not end the session/i)
    del.mockRestore()
  })

  it('says so when the list itself cannot load', async () => {
    const state = freshState()
    installAxiosMock(state)
    vi.spyOn(http, 'get').mockRejectedValue(new Error('down'))
    renderWithProviders(<SessionsSection onSignOut={() => {}} onSignOutEverywhere={() => {}} />)
    expect(await screen.findByText(/could not load your sessions/i)).toBeInTheDocument()
  })
})

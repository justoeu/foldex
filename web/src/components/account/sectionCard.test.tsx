import { describe, it, expect, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Notice, SectionCard, SectionRow } from './SectionCard'
import { SessionsSection } from './SessionsSection'
import { renderWithProviders } from '../../test/renderWithProviders'
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
  it('tells the two sign-outs apart by what each one ends', () => {
    renderWithProviders(<SessionsSection onSignOut={() => {}} onSignOutEverywhere={() => {}} />)

    // The distinction the panel exists to make. Two buttons whose labels differ
    // by one word is not enough for an action that ends every device.
    const here = screen.getByRole('group', { name: /^sign out$/i })
    expect(within(here).getByText(/this browser only/i)).toBeInTheDocument()

    const everywhere = screen.getByRole('group', { name: /sign out everywhere/i })
    expect(within(everywhere).getByText(/every device/i)).toBeInTheDocument()
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

    const here = screen.getByRole('group', { name: /^sign out$/i })
    await user.click(within(here).getByRole('button'))
    expect(one).toHaveBeenCalledTimes(1)
    expect(all).not.toHaveBeenCalled()

    const everywhere = screen.getByRole('group', { name: /sign out everywhere/i })
    await user.click(within(everywhere).getByRole('button'))
    expect(all).toHaveBeenCalledTimes(1)
  })
})

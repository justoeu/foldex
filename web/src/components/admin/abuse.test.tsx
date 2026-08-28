import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AbuseSection } from './AbuseSection'
import { freshState, installAxiosMock, type MockState } from '../../test/server'
import { renderWithProviders, testAdminSession } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import type { SessionState } from '../../auth/types'

const ownerSession = {
  ...testAdminSession,
  user: { ...(testAdminSession as never as { user: object }).user, role: 'owner' },
} as SessionState

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

/** The band a field belongs to, by its accessible name. */
function band(name: RegExp) {
  return screen.getByRole('region', { name })
}

describe('AbuseSection — the limits come from the payload', () => {
  it('renders each knob under the range the SERVER advertised', async () => {
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    const input = await screen.findByLabelText(/failures per account/i)
    expect(input).toHaveAttribute('min', '3')
    expect(input).toHaveAttribute('max', '50')
    expect(
      within(band(/sign-in/i)).getByText(/between 3 and 50 · default 5/i),
    ).toBeInTheDocument()
  })

  // The whole reason bounds ride the payload: a second copy in TypeScript is
  // the copy that goes stale. Move the server's numbers and the screen moves.
  it('follows the payload when the server tightens a bound', async () => {
    vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
      if (!url.startsWith('/api/admin/abuse-policy')) return { data: {} } as never
      return {
        data: {
          policy: {
            login_distinct_accounts_per_ip: 10, login_failures_per_account: 5,
            login_window_minutes: 15, api_writes_per_minute: 120,
            api_expensive_per_hour: 20, public_click_coalesce_seconds: 10,
            anomaly_spray_accounts: 10, anomaly_hammer_failures: 20,
            anomaly_window_minutes: 15,
          },
          bounds: [{ field: 'login_failures_per_account', min: 4, max: 9, default: 6 }],
          observed: {
            window_days: 30, max_distinct_accounts_per_ip: 0,
            max_failures_per_account: 0, peak_writes_per_minute: 0,
          },
          can_write: true,
        },
      } as never
    })
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    const input = await screen.findByLabelText(/failures per account/i)
    expect(input).toHaveAttribute('min', '4')
    expect(input).toHaveAttribute('max', '9')
    expect(
      within(band(/sign-in/i)).getByText(/between 4 and 9 · default 6/i),
    ).toBeInTheDocument()
  })
})

describe('AbuseSection — a degraded payload', () => {
  // A knob the server described no range for still renders. Dropping it would
  // hide a setting the policy carries because an aggregate went missing, which
  // reads as a broken screen rather than as the thin payload it is.
  it('renders a knob the bounds said nothing about, without inventing a range', async () => {
    vi.spyOn(http, 'get').mockResolvedValue({
      data: {
        policy: {
          login_distinct_accounts_per_ip: 10, login_failures_per_account: 5,
          login_window_minutes: 15, api_writes_per_minute: 120,
          api_expensive_per_hour: 20, public_click_coalesce_seconds: null,
          anomaly_spray_accounts: 10, anomaly_hammer_failures: 20,
          anomaly_window_minutes: 15,
        },
        bounds: [],
        observed: {
          window_days: 30, max_distinct_accounts_per_ip: 0,
          max_failures_per_account: 0, peak_writes_per_minute: 0,
        },
        can_write: true,
      },
    } as never)
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    const input = await screen.findByLabelText(/failures per account/i)
    expect(input).toHaveValue(5)
    expect(input).not.toHaveAttribute('min')
    expect(input).not.toHaveAttribute('max')
    expect(screen.queryByText(/between .* and .* · default/i)).not.toBeInTheDocument()
    // The nullable knob has no default to resolve to either, and 0 is the one
    // value that is honest about that: coalescing off.
    expect(screen.getByLabelText(/coalesc/i)).toHaveValue(0)
  })
})

describe('AbuseSection — a reader who may not write', () => {
  // Disabled and EXPLAINED, never hidden: a form that vanishes teaches the
  // reader the screen is broken, and INV-168's rule is that a cell the grid
  // cannot offer says why.
  it('disables every field and names the reason', async () => {
    state.abuseCanWrite = false
    renderWithProviders(<AbuseSection />)

    expect(await screen.findByText(/only the instance owner/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/failures per account/i)).toBeDisabled()
    expect(screen.getByLabelText(/writes per minute/i)).toBeDisabled()
    expect(screen.queryByRole('button', { name: /^save$/i })).not.toBeInTheDocument()
  })
})

describe('AbuseSection — a refused write', () => {
  // The server's sentence names the field and the two real numbers on purpose.
  // Rewriting it removes the only part that tells the owner what to type.
  it('shows the refusal verbatim', async () => {
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    const input = await screen.findByLabelText(/distinct accounts/i)
    const user = userEvent.setup()
    await user.clear(input)
    await user.type(input, '2')
    await user.click(screen.getByRole('button', { name: /^save$/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'login_distinct_accounts_per_ip must be between 3 and 100, got 2',
    )
  })
})

describe('AbuseSection — restore defaults', () => {
  it('puts a band back to the defaults the payload carries', async () => {
    state.abusePolicy = { login_failures_per_account: 41, api_writes_per_minute: 4000 }
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    const failures = await screen.findByLabelText(/failures per account/i)
    expect(failures).toHaveValue(41)

    await userEvent.setup().click(
      within(band(/sign-in/i)).getByRole('button', { name: /restore defaults/i }),
    )

    expect(failures).toHaveValue(5)
    // Only the band that was restored: the API band keeps what was stored.
    expect(screen.getByLabelText(/writes per minute/i)).toHaveValue(4000)
  })
})

describe('AbuseSection — what this instance has seen', () => {
  it('names the observed peak beside the knob it informs', async () => {
    state.abuseObserved = { max_failures_per_account: 7, peak_writes_per_minute: 340 }
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    await screen.findByLabelText(/failures per account/i)
    expect(
      within(band(/sign-in/i)).getByText(/highest observed in 30 days: 7/i),
    ).toBeInTheDocument()
    expect(
      within(band(/authenticated api/i)).getByText(/highest observed in 30 days: 340/i),
    ).toBeInTheDocument()
  })

  // Zero is "nothing happened", not "the peak was zero". Printing the digit
  // would read as a measurement and invite tuning against it.
  it('says there is no data rather than printing a zero', async () => {
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    await screen.findByLabelText(/failures per account/i)
    const login = within(band(/sign-in/i))
    expect(login.getAllByText(/no data yet/i).length).toBeGreaterThan(0)
    expect(login.queryByText(/highest observed in 30 days: 0/i)).not.toBeInTheDocument()
  })
})

describe('AbuseSection — the number that reports and the number that acts', () => {
  // The most important sentence on the screen. Without it an operator tightens
  // the anomaly threshold believing they tightened the defence.
  it('states that the detection band blocks nothing', async () => {
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    const detection = within(await screen.findByRole('region', { name: /anomaly detection/i }))
    expect(detection.getByText(/block nothing/i)).toBeInTheDocument()
    expect(detection.getByText(/what the panel calls anomalous/i)).toBeInTheDocument()
  })

  it('says that zero seconds turns coalescing off', async () => {
    renderWithProviders(<AbuseSection />, { session: ownerSession })
    const publicBand = within(await screen.findByRole('region', { name: /public surface/i }))
    expect(publicBand.getByText(/0 turns it off/i)).toBeInTheDocument()
  })

  // The one nullable knob: null asks for the default, and the form must show a
  // number rather than an empty box the owner would save as zero by accident.
  it('resolves a null coalesce window to the advertised default', async () => {
    state.abusePolicy = { public_click_coalesce_seconds: null }
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    expect(await screen.findByLabelText(/coalesc/i)).toHaveValue(10)
  })
})

describe('AbuseSection — a saved change', () => {
  it('sends the whole document and keeps what came back', async () => {
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    const input = await screen.findByLabelText(/expensive operations/i)
    const user = userEvent.setup()
    await user.clear(input)
    await user.type(input, '45')
    await user.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(state.abusePolicyPuts?.length).toBe(1))
    expect(state.abusePolicyPuts?.[0]).toMatchObject({
      api_expensive_per_hour: 45,
      login_failures_per_account: 5,
    })
    expect(await screen.findByText(/limits saved/i)).toBeInTheDocument()
  })

  // A network failure carries no envelope, so there is no server sentence to
  // render. The screen falls back to its own wording rather than an empty
  // alert — the verbatim rule is about not REWRITING a message, not about
  // pretending one arrived.
  it('falls back to its own wording when the failure carries no message', async () => {
    vi.spyOn(http, 'put').mockRejectedValue(new Error('network down'))
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    await screen.findByLabelText(/failures per account/i)
    await userEvent.setup().click(screen.getByRole('button', { name: /^save$/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/limits could not be saved/i)
  })

  it('reports an unreachable policy instead of an empty screen', async () => {
    vi.spyOn(http, 'get').mockRejectedValue(new Error('boom'))
    renderWithProviders(<AbuseSection />, { session: ownerSession })

    expect(await screen.findByText(/limits could not be loaded/i)).toBeInTheDocument()
  })
})

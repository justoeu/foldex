import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { QueryClient } from '@tanstack/react-query'
import type { InstancePolicy } from '../api/admin'
import { http } from '../api/client'
import { InviteScreen } from '../components/auth/InviteScreen'
import { ResetScreen } from '../components/auth/ResetScreen'
import { PasswordRow } from '../components/account/PasswordCard'
import { accountErrorMessage } from '../components/account/accountErrors'
import { INSTANCE_POLICY_KEY } from '../hooks/useInstancePolicy'
import { makeQueryClient, renderWithProviders, testAdminUser } from '../test/renderWithProviders'
import type { InvitePreview } from '../api/auth'
import type { SessionState } from './types'

/**
 * DUP-ECH-002: every password surface re-derived the compiled-in 8. Stubbing
 * the live policy to 12 is what makes a screen still saying 8 a failure
 * rather than a coincidence with the default.
 */
const LIVE_FLOOR = 12
const UNDER_FLOOR = 'abcdefghij' // 10
const AT_FLOOR = 'abcdefghijkl' // 12

const anonymous: SessionState = {
  status: 'anonymous',
  features: { google_oauth: false, two_factor: false, email_delivery: false },
}

const preview: InvitePreview = {
  email: 'new@example.com',
  role: 'editor',
  expires_at: '2026-08-15T12:00:00Z',
}

function policyDoc(minLen: number): InstancePolicy {
  return {
    admin_second_factor: 'any',
    password_min_length: minLen,
    otp_ttl_minutes: 5,
    otp_cooldown_seconds: 60,
    google_allowed_domains: [],
    google_auto_provision: false,
    google_default_role: 'editor',
  }
}

function stubPolicy(client: QueryClient, minLen = LIVE_FLOOR) {
  const policy = policyDoc(minLen)
  client.setQueryData(INSTANCE_POLICY_KEY, policy)
  vi.spyOn(http, 'get').mockImplementation(((url: string) => {
    if (url === '/api/admin/policy') return Promise.resolve({ data: policy })
    return Promise.reject(new Error(`unexpected GET ${url}`))
  }) as never)
  return client
}

function mockInviteLookup() {
  return vi.spyOn(http, 'post').mockImplementation(((url: string) => {
    if (url === '/api/auth/invites/lookup') return Promise.resolve({ data: preview })
    if (url === '/api/auth/invites/accept') {
      return Promise.reject(new Error('accept must not run under the live floor'))
    }
    return Promise.reject(new Error(`unexpected POST ${url}`))
  }) as never)
}

function rejectWith(status: number, code: string) {
  return Promise.reject({ response: { status, data: { error: { code } } } })
}

const t = (key: string, opts?: Record<string, unknown>) =>
  opts && 'count' in opts ? `${key}:${String(opts.count)}` : key

afterEach(() => vi.restoreAllMocks())

describe('password floor copy and client block follow instance policy', () => {
  it('InviteScreen hints and blocks at 12, not 8', async () => {
    const client = stubPolicy(makeQueryClient())
    const post = mockInviteLookup()
    renderWithProviders(<InviteScreen token="TOK" onGiveUp={() => {}} />, {
      client,
      session: anonymous,
    })

    await screen.findByDisplayValue(preview.email)
    expect(await screen.findByText(/at least 12 characters/i)).toBeInTheDocument()
    expect(screen.queryByText(/at least 8 characters/i)).not.toBeInTheDocument()

    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/^password$/i), UNDER_FLOOR)
    await user.type(screen.getByLabelText(/confirm password/i), UNDER_FLOOR)

    const submit = screen.getByRole('button', { name: /activate and sign in/i })
    expect(submit).toBeDisabled()
    expect(post).not.toHaveBeenCalledWith(
      '/api/auth/invites/accept',
      expect.anything(),
      expect.anything(),
    )
  })

  it('PasswordCard blocks a 10-character value when the floor is 12', async () => {
    const client = stubPolicy(makeQueryClient())
    renderWithProviders(
      <PasswordRow user={{ ...testAdminUser, has_password: true, totp_enabled: false }} />,
      { client },
    )

    const user = userEvent.setup()
    const row = within(screen.getByRole('group', { name: /^password$/i }))
    await user.click(row.getByRole('button', { name: /change password/i }))
    await user.type(row.getByLabelText(/current password/i), 'old-password-here')
    await user.type(row.getByLabelText(/^new password$/i), UNDER_FLOOR)
    await user.type(row.getByLabelText(/confirm the password/i), UNDER_FLOOR)

    await waitFor(() => expect(row.getByRole('button', { name: /save password/i })).toBeDisabled())
  })

  it('PasswordCard interpolates password_too_short from the live floor', async () => {
    const client = stubPolicy(makeQueryClient())
    vi.spyOn(http, 'post').mockImplementation(() => rejectWith(400, 'password_too_short') as never)

    renderWithProviders(
      <PasswordRow user={{ ...testAdminUser, has_password: true, totp_enabled: false }} />,
      { client },
    )

    const user = userEvent.setup()
    const row = within(screen.getByRole('group', { name: /^password$/i }))
    await user.click(row.getByRole('button', { name: /change password/i }))
    await user.type(row.getByLabelText(/current password/i), 'old-password-here')
    await user.type(row.getByLabelText(/^new password$/i), AT_FLOOR)
    await user.type(row.getByLabelText(/confirm the password/i), AT_FLOOR)
    await user.click(row.getByRole('button', { name: /save password/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/at least 12 characters/i)
  })

  it('ResetScreen blocks under the live floor and interpolates 12', async () => {
    const client = stubPolicy(makeQueryClient())
    const post = vi.spyOn(http, 'post').mockImplementation(() => rejectWith(400, 'password_too_short') as never)

    renderWithProviders(<ResetScreen token="TOK" onGiveUp={() => {}} />, {
      client,
      session: anonymous,
    })

    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/^new password$/i), UNDER_FLOOR)
    await user.type(screen.getByLabelText(/confirm new password/i), UNDER_FLOOR)

    const submit = screen.getByRole('button', { name: /save and sign in/i })
    await waitFor(() => expect(submit).toBeDisabled())
    expect(post).not.toHaveBeenCalled()

    await user.clear(screen.getByLabelText(/^new password$/i))
    await user.clear(screen.getByLabelText(/confirm new password/i))
    await user.type(screen.getByLabelText(/^new password$/i), AT_FLOOR)
    await user.type(screen.getByLabelText(/confirm new password/i), AT_FLOOR)
    await waitFor(() => expect(submit).toBeEnabled())
    await user.click(submit)

    expect(await screen.findByRole('alert')).toHaveTextContent(/at least 12 characters/i)
  })

  it('accountErrorMessage interpolates the policy count, not 8', () => {
    expect(
      accountErrorMessage(
        { response: { status: 400, data: { error: { code: 'password_too_short' } } } },
        t,
        LIVE_FLOOR,
      ),
    ).toBe('auth_errors.password_too_short:12')
  })

  it('InviteScreen follows /me features when the admin policy document 404s', async () => {
    vi.spyOn(http, 'get').mockRejectedValue({ response: { status: 404 } })
    mockInviteLookup()
    renderWithProviders(<InviteScreen token="TOK" onGiveUp={() => {}} />, {
      client: makeQueryClient(),
      session: {
        ...anonymous,
        features: { ...anonymous.features, password_min_length: LIVE_FLOOR },
      },
    })

    await screen.findByDisplayValue(preview.email)
    expect(await screen.findByText(/at least 12 characters/i)).toBeInTheDocument()
  })
})

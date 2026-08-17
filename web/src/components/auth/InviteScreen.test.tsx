import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as auth from '../../api/auth'
import type { InvitePreview, MeResponse } from '../../api/auth'
import { http } from '../../api/client'
import { useAuth } from '../../auth/AuthProvider'
import type { SessionState } from '../../auth/types'
import { renderWithProviders } from '../../test/renderWithProviders'
import { InviteScreen } from './InviteScreen'

const anonymous: SessionState = {
  status: 'anonymous',
  features: { google_oauth: false, two_factor: false, email_delivery: false },
}

const preview: InvitePreview = {
  email: 'new@example.com',
  role: 'editor',
  expires_at: '2026-08-15T12:00:00Z',
}

const authenticated: MeResponse = {
  status: 'authenticated',
  user: {
    id: 7,
    email: preview.email,
    name: 'New User',
    role: 'editor',
    status: 'active',
    has_password: true,
    totp_enabled: false,
    created_at: '2026-08-14T12:00:00Z',
  },
  csrf_token: 'csrf',
  features: anonymous.features,
}

type MaybePromise<T> = T | Promise<T>
type Handlers = {
  lookup?: (token: string, signal?: AbortSignal) => MaybePromise<InvitePreview>
  accept?: (input: { token: string; name: string; password: string }, signal?: AbortSignal) => MaybePromise<MeResponse>
}

function mockInviteRequests(handlers: Handlers = {}) {
  return vi.spyOn(http, 'post').mockImplementation(((
    url: string,
    body: unknown,
    config?: { signal?: AbortSignal },
  ) => {
    if (url === '/api/auth/invites/lookup') {
      const token = (body as { token: string }).token
      return Promise.resolve((handlers.lookup ?? (() => preview))(token, config?.signal)).then(
        (data) => ({ data }),
      )
    }
    if (url === '/api/auth/invites/accept') {
      return Promise.resolve(
        (handlers.accept ?? (() => authenticated))(
          body as { token: string; name: string; password: string },
          config?.signal,
        ),
      ).then((data) => ({ data }))
    }
    return Promise.reject(new Error(`unexpected POST ${url}`))
  }) as never)
}

function SessionProbe() {
  const { session } = useAuth()
  return <span data-testid="session-status">{session.status}</span>
}

function inviteView(token = 'TOK', onGiveUp = vi.fn(), session = anonymous) {
  const view = renderWithProviders(
    <>
      <InviteScreen token={token} onGiveUp={onGiveUp} />
      <SessionProbe />
    </>,
    { session },
  )
  return { ...view, onGiveUp }
}

async function fillForm(password = 'a good password', confirm = password) {
  const user = userEvent.setup()
  await screen.findByDisplayValue(preview.email)
  await user.type(screen.getByLabelText(/^name$/i), 'New User')
  await user.type(screen.getByLabelText(/^password$/i), password)
  await user.type(screen.getByLabelText(/confirm password/i), confirm)
  return user
}

function rejectWith(status: number, code: string) {
  return Promise.reject({ response: { status, data: { error: { code } } } })
}

afterEach(() => vi.restoreAllMocks())

describe('InviteScreen', () => {
  it('looks up the token, submits the expected request, and adopts the session', async () => {
    const post = mockInviteRequests()
    inviteView()
    const user = await fillForm()

    expect(post).toHaveBeenCalledWith(
      '/api/auth/invites/lookup',
      { token: 'TOK' },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )

    await user.click(screen.getByRole('button', { name: /activate and sign in/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/invites/accept', {
        token: 'TOK',
        name: 'New User',
        password: 'a good password',
      }, expect.objectContaining({ signal: expect.any(AbortSignal) })),
    )
    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('authenticated'),
    )
  })

  it('does not request acceptance when the passwords differ', async () => {
    const post = mockInviteRequests()
    inviteView()
    const user = await fillForm('a good password', 'a different password')
    post.mockClear()

    await user.click(screen.getByRole('button', { name: /activate and sign in/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/do not match/i)
    expect(post).not.toHaveBeenCalled()
  })

  it('suppresses two submit events while acceptance is in flight', async () => {
    let resolveAccept!: (response: MeResponse) => void
    const post = mockInviteRequests({
      accept: () => new Promise((resolve) => { resolveAccept = resolve }),
    })
    inviteView()
    await fillForm()
    post.mockClear()

    const form = screen.getByRole('button', { name: /activate and sign in/i }).closest('form')
    expect(form).not.toBeNull()
    act(() => {
      fireEvent.submit(form!)
      fireEvent.submit(form!)
    })

    expect(post).toHaveBeenCalledTimes(1)
    await act(async () => resolveAccept(authenticated))
  })

  it('disables Google acceptance while password acceptance is in flight', async () => {
    let resolveAccept!: (response: MeResponse) => void
    mockInviteRequests({
      accept: () => new Promise((resolve) => { resolveAccept = resolve }),
    })
    inviteView('TOK', vi.fn(), {
      ...anonymous,
      features: { ...anonymous.features, google_oauth: true },
    })
    const user = await fillForm()

    await user.click(screen.getByRole('button', { name: /activate and sign in/i }))

    expect(screen.getByRole('button', { name: /continue with google/i })).toBeDisabled()
    await act(async () => resolveAccept(authenticated))
  })

  it('prevents password acceptance while Google acceptance is starting', async () => {
    let resolveGoogle!: () => void
    vi.spyOn(auth, 'startGoogleOAuth').mockImplementation(
      () => new Promise<void>((resolve) => { resolveGoogle = resolve }),
    )
    const post = mockInviteRequests()
    inviteView('TOK', vi.fn(), {
      ...anonymous,
      features: { ...anonymous.features, google_oauth: true },
    })
    await screen.findByDisplayValue(preview.email)
    post.mockClear()

    await userEvent.setup().click(screen.getByRole('button', { name: /continue with google/i }))

    const submit = screen.getByRole('button', { name: /activate and sign in/i })
    expect(submit).toBeDisabled()
    fireEvent.submit(submit.closest('form')!)
    expect(post).not.toHaveBeenCalledWith('/api/auth/invites/accept', expect.anything(), expect.anything())
    await act(async () => resolveGoogle())
  })

  it.each([
    [404, 'invite_invalid', /expired|revoked|already been used/i],
    [409, 'email_taken', /already registered/i],
    [400, 'password_too_short', /at least 8 characters/i],
    [400, 'password_too_long', /maximum 72 bytes/i],
    [429, 'too_many_attempts', /too many attempts/i],
    [0, '', /could not reach the server/i],
  ])('maps acceptance failure %s/%s', async (status, code, expected) => {
    mockInviteRequests({
      accept: () => status === 0 ? Promise.reject({ request: {} }) : rejectWith(status, code),
    })
    inviteView()
    const user = await fillForm()

    await user.click(screen.getByRole('button', { name: /activate and sign in/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(expected)
    expect(screen.getByRole('button', { name: /activate and sign in/i })).toBeEnabled()
  })

  it.each(['invite_invalid', 'not_found'])('treats known %s lookup failures as invalid', async (code) => {
    mockInviteRequests({ lookup: () => rejectWith(404, code) })
    inviteView('DEAD')

    expect(
      await screen.findByRole('heading', { name: /invitation is no longer valid/i }),
    ).toBeInTheDocument()
  })

  it('keeps a token after a transient lookup failure and retries it', async () => {
    const tokens: string[] = []
    let attempts = 0
    mockInviteRequests({
      lookup: (token) => {
        tokens.push(token)
        attempts += 1
        if (attempts === 1) return Promise.reject({ request: {} })
        return preview
      },
    })
    const { onGiveUp } = inviteView('RETRY-TOKEN')
    const user = userEvent.setup()

    expect(await screen.findByRole('alert')).toHaveTextContent(/could not reach the server/i)
    expect(screen.queryByRole('heading', { name: /no longer valid/i })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /try again/i }))

    expect(await screen.findByDisplayValue(preview.email)).toBeInTheDocument()
    expect(tokens).toEqual(['RETRY-TOKEN', 'RETRY-TOKEN'])
    expect(onGiveUp).not.toHaveBeenCalled()
  })

  it('retries the same token after a lookup server error', async () => {
    let attempts = 0
    mockInviteRequests({
      lookup: () => {
        attempts += 1
        if (attempts === 1) return rejectWith(503, 'server_error')
        return preview
      },
    })
    inviteView('SERVER-RETRY')
    const user = userEvent.setup()

    expect(await screen.findByRole('alert')).toHaveTextContent(/something went wrong/i)
    await user.click(screen.getByRole('button', { name: /try again/i }))

    expect(await screen.findByDisplayValue(preview.email)).toBeInTheDocument()
    expect(attempts).toBe(2)
  })

  it('clears the previous preview while a new token is being checked', async () => {
    let resolveSecond!: (value: InvitePreview) => void
    mockInviteRequests({
      lookup: (token) => token === 'FIRST'
        ? { ...preview, email: 'first@example.com' }
        : new Promise((resolve) => { resolveSecond = resolve }),
    })
    const { rerender } = inviteView('FIRST')
    await screen.findByDisplayValue('first@example.com')

    rerender(
      <>
        <InviteScreen token="SECOND" onGiveUp={() => {}} />
        <SessionProbe />
      </>,
    )

    expect(screen.queryByDisplayValue('first@example.com')).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(/loading/i)
    await act(async () => resolveSecond({ ...preview, email: 'second@example.com' }))
    expect(await screen.findByDisplayValue('second@example.com')).toBeInTheDocument()
  })

  it('cancels an obsolete lookup and ignores its late result', async () => {
    let firstSignal: AbortSignal | undefined
    let resolveFirst!: (value: InvitePreview) => void
    mockInviteRequests({
      lookup: (token, signal) => {
        if (token === 'FIRST') {
          firstSignal = signal
          return new Promise((resolve) => { resolveFirst = resolve })
        }
        return { ...preview, email: 'second@example.com' }
      },
    })
    const { rerender } = inviteView('FIRST')
    await waitFor(() => expect(firstSignal).toBeDefined())

    rerender(
      <>
        <InviteScreen token="SECOND" onGiveUp={() => {}} />
        <SessionProbe />
      </>,
    )

    expect(firstSignal?.aborted).toBe(true)
    expect(await screen.findByDisplayValue('second@example.com')).toBeInTheDocument()
    await act(async () => resolveFirst({ ...preview, email: 'stale@example.com' }))
    expect(screen.queryByDisplayValue('stale@example.com')).not.toBeInTheDocument()
    expect(screen.getByDisplayValue('second@example.com')).toBeInTheDocument()
  })

  it('cancels its lookup when unmounted', async () => {
    let signal: AbortSignal | undefined
    mockInviteRequests({
      lookup: (_token, requestSignal) => {
        signal = requestSignal
        return new Promise(() => {})
      },
    })
    const { unmount } = inviteView()
    await waitFor(() => expect(signal).toBeDefined())

    unmount()

    expect(signal?.aborted).toBe(true)
  })

  it('aborts and ignores acceptance from an obsolete token', async () => {
    let acceptSignal: AbortSignal | undefined
    let resolveAccept!: (value: MeResponse) => void
    mockInviteRequests({
      lookup: (token) => token === 'FIRST' ? preview : { ...preview, email: 'second@example.com' },
      accept: (_input, signal) => {
        acceptSignal = signal
        return new Promise((resolve) => { resolveAccept = resolve })
      },
    })
    const { rerender } = inviteView('FIRST')
    const user = await fillForm()
    await user.click(screen.getByRole('button', { name: /activate and sign in/i }))
    await waitFor(() => expect(acceptSignal).toBeDefined())

    rerender(
      <>
        <InviteScreen token="SECOND" onGiveUp={() => {}} />
        <SessionProbe />
      </>,
    )

    expect(acceptSignal?.aborted).toBe(true)
    expect(await screen.findByDisplayValue('second@example.com')).toBeInTheDocument()
    await act(async () => resolveAccept(authenticated))
    expect(screen.getByTestId('session-status')).toHaveTextContent('anonymous')
    expect(screen.getByRole('button', { name: /activate and sign in/i })).toBeEnabled()
  })
})

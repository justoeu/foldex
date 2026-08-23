import { describe, it, expect, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { useAvailability, type AvailabilityResponse } from './useAvailability'

/** The hook debounces at 450 ms; every case has to outlast it. */
async function settle() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 600))
  })
}

const answering = (data: AvailabilityResponse) => vi.fn(async () => data)

describe('the availability probe', () => {
  it('reports a free value', async () => {
    const { result } = renderHook(() => useAvailability(answering({ available: true }), 'valmir'))
    await settle()
    await waitFor(() => expect(result.current.state).toBe('free'))
  })

  it("carries the server's reason, so the field can say what to fix", async () => {
    const probe = answering({ available: false, reason: 'reserved' })
    const { result } = renderHook(() => useAvailability(probe, 'admin'))
    await settle()
    await waitFor(() =>
      expect(result.current).toEqual({ state: 'refused', reason: 'reserved' }))
  })

  // A reason on an AVAILABLE answer is a warning, not a refusal: the server
  // would accept the write, and a screen hiding a button the server allows
  // reads as a missing feature.
  it('separates a warning from a refusal', async () => {
    const probe = answering({ available: true, reason: 'pending' })
    const { result } = renderHook(() => useAvailability(probe, 'someone@example.com'))
    await settle()
    await waitFor(() => expect(result.current).toEqual({ state: 'warn', reason: 'pending' }))
  })

  it('falls back to "taken" when a refusal carries no reason', async () => {
    const { result } = renderHook(() => useAvailability(answering({ available: false }), 'x'))
    await settle()
    await waitFor(() => expect(result.current).toEqual({ state: 'refused', reason: 'taken' }))
  })

  // Typing back to the value the account already holds is not a claim, so a
  // green check there would report on an action the user is not taking.
  it('stays idle on the value the account already holds', async () => {
    const probe = answering({ available: true })
    const { result } = renderHook(() => useAvailability(probe, ' Valmir ', 'valmir'))
    await settle()
    expect(result.current.state).toBe('idle')
    expect(probe).not.toHaveBeenCalled()
  })

  it('asks nothing while the field is empty', async () => {
    const probe = answering({ available: true })
    const { result } = renderHook(() => useAvailability(probe, '   '))
    await settle()
    expect(result.current.state).toBe('idle')
    expect(probe).not.toHaveBeenCalled()
  })

  // The endpoint is rate-limited BY DESIGN — its budget is what keeps it from
  // being an enumeration API — so a field that fired per character would burn a
  // human's whole allowance halfway through one word.
  it('sends one request for a burst of keystrokes', async () => {
    const probe = answering({ available: true })
    const { rerender } = renderHook(({ v }) => useAvailability(probe, v), {
      initialProps: { v: 'v' },
    })
    for (const v of ['va', 'val', 'valm', 'valmi', 'valmir']) rerender({ v })
    await settle()
    expect(probe).toHaveBeenCalledTimes(1)
    expect(probe).toHaveBeenCalledWith('valmir', expect.any(AbortSignal))
  })

  // A failed probe is not a verdict on the value. Painting it as a refusal
  // would tell the user their input is wrong when the network is.
  it('reports an error separately from a refusal', async () => {
    const probe = vi.fn(async () => {
      throw new Error('offline')
    })
    const { result } = renderHook(() => useAvailability(probe, 'valmir'))
    await settle()
    await waitFor(() => expect(result.current.state).toBe('error'))
  })

  // abort() is a no-op on a promise that already settled, so a response landing
  // just before the next keystroke still resolves. Without the guard on the
  // SUCCESS arm it writes an answer about the previous value — and since the
  // save button is gated on this state, the other ordering clears a legitimate
  // refusal and re-enables Save.
  it('discards an answer that lands after the value moved on', async () => {
    let release: (v: AvailabilityResponse) => void = () => {}
    const probe = vi.fn(
      (value: string) =>
        value === 'first'
          ? new Promise<AvailabilityResponse>((res) => {
              release = res
            })
          : Promise.resolve<AvailabilityResponse>({ available: false, reason: 'taken' }),
    )

    const { result, rerender } = renderHook(({ v }) => useAvailability(probe, v), {
      initialProps: { v: 'first' },
    })
    await settle()

    rerender({ v: 'second' })
    await settle()
    await waitFor(() => expect(result.current).toEqual({ state: 'refused', reason: 'taken' }))

    // The stale request answers now. It must change nothing.
    await act(async () => {
      release({ available: true })
      await Promise.resolve()
    })
    expect(result.current).toEqual({ state: 'refused', reason: 'taken' })
  })

  // Rejecting after abort is the ordinary shape of a cancelled request; it must
  // not paint an error under the field on every keystroke.
  it('stays quiet when an aborted request rejects', async () => {
    const probe = vi.fn(
      (_value: string, signal: AbortSignal) =>
        new Promise<AvailabilityResponse>((_res, rej) => {
          signal.addEventListener('abort', () => rej(new Error('canceled')))
        }),
    )
    const { result, rerender } = renderHook(({ v }) => useAvailability(probe, v), {
      initialProps: { v: 'first' },
    })
    await settle()
    rerender({ v: 'second' })
    await act(async () => {
      await Promise.resolve()
    })
    expect(result.current.state).toBe('checking')
  })
})

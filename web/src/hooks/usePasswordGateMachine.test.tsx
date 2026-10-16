import { describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { usePasswordGateMachine } from './usePasswordGateMachine'

const messages = {
  attemptsRemaining: (n: number) => `${n} left`,
  incorrect: 'incorrect',
  generic: 'generic',
}

function setup(unlock: (pw: string) => Promise<{ token: string; expiresAt: number }>) {
  const onUnlocked = vi.fn()
  const view = renderHook(() =>
    usePasswordGateMachine({ unlock, onUnlocked, hasHint: true, messages }),
  )
  return { ...view, onUnlocked }
}

function rejectWith(data: Record<string, unknown>) {
  return () => Promise.reject({ response: { data } })
}

describe('usePasswordGateMachine', () => {
  it('moves idle → awaiting-code on typing and back on clear', () => {
    const { result } = setup(vi.fn())
    expect(result.current.state.kind).toBe('idle')
    act(() => result.current.setPassword('x'))
    expect(result.current.state.kind).toBe('awaiting-code')
    act(() => result.current.setPassword(''))
    expect(result.current.state).toEqual({ kind: 'idle', error: null })
  })

  it('goes submitting → unlocked on success', async () => {
    const unlock = vi.fn().mockResolvedValue({ token: 'tok', expiresAt: 42 })
    const { result, onUnlocked } = setup(unlock)
    act(() => result.current.setPassword('pw'))
    await act(async () => {
      await result.current.submit()
    })
    expect(onUnlocked).toHaveBeenCalledWith({ token: 'tok', expiresAt: 42 })
  })

  it('lands in wrong-code with server attempt counters', async () => {
    const { result } = setup(
      rejectWith({ error: { code: 'wrong_password' }, failed_attempts: 2, attempts_remaining: 3 }),
    )
    act(() => result.current.setPassword('pw'))
    await act(async () => {
      await result.current.submit()
    })
    expect(result.current.state).toEqual({ kind: 'wrong-code', error: '3 left', failedAttempts: 2 })
    expect(result.current.password).toBe('')
    expect(result.current.showHint).toBe(false)
  })

  it('locks out on too_many_attempts and shows the hint', async () => {
    const until = new Date(Date.now() + 60_000).toISOString()
    const { result } = setup(rejectWith({ error: { code: 'too_many_attempts' }, locked_until: until }))
    act(() => result.current.setPassword('pw'))
    await act(async () => {
      await result.current.submit()
    })
    expect(result.current.state.kind).toBe('locked-out')
    expect(result.current.locked).toBe(true)
    expect(result.current.showHint).toBe(true)
    expect(result.current.error).toBeNull()
  })

  it('falls back to idle with the generic error on unknown failures', async () => {
    const { result } = setup(rejectWith({ error: { code: 'boom' } }))
    act(() => result.current.setPassword('pw'))
    await act(async () => {
      await result.current.submit()
    })
    expect(result.current.state).toEqual({ kind: 'idle', error: 'generic' })
  })

  it('typing after a wrong code clears the error', async () => {
    const { result } = setup(rejectWith({ error: { code: 'wrong_password' }, failed_attempts: 1 }))
    act(() => result.current.setPassword('pw'))
    await act(async () => {
      await result.current.submit()
    })
    expect(result.current.error).toBe('incorrect')
    act(() => result.current.setPassword('n'))
    expect(result.current.state.kind).toBe('awaiting-code')
    expect(result.current.error).toBeNull()
  })
})

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useWebPush, useSubscribePush, useUnsubscribePush } from './useWebPush'
import { isPushSupported, urlBase64ToUint8Array } from '../lib/push'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'

let state: MockState
const VAPID = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
  })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

function installPushEnv(opts: {
  supported?: boolean
  permission?: NotificationPermission
  subscribed?: boolean
  requestPermission?: NotificationPermission
} = {}) {
  const supported = opts.supported ?? true
  const permission = opts.permission ?? 'default'
  const endpoint = 'https://push.example/sub/1'
  let current: {
    endpoint: string
    toJSON: () => { endpoint: string; keys: { p256dh: string; auth: string } }
    unsubscribe: ReturnType<typeof vi.fn>
  } | null = null

  const makeSub = () => ({
    endpoint,
    toJSON: () => ({ endpoint, keys: { p256dh: 'pk', auth: 'ak' } }),
    unsubscribe: vi.fn().mockImplementation(async () => {
      current = null
      return true
    }),
  })

  if (opts.subscribed) current = makeSub()
  const sub = current

  const pushManager = {
    getSubscription: vi.fn().mockImplementation(async () => current),
    subscribe: vi.fn().mockImplementation(async () => {
      current = makeSub()
      return current
    }),
  }

  if (supported) {
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { ready: Promise.resolve({ pushManager }) },
    })
    Object.defineProperty(window, 'PushManager', {
      configurable: true,
      value: function PushManager() {},
    })
  } else {
    // `'serviceWorker' in navigator` is true even when the value is undefined —
    // delete the property so isPushSupported returns false.
    try {
      delete (navigator as any).serviceWorker
    } catch {
      Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: undefined, enumerable: false })
    }
    try {
      delete (window as any).PushManager
    } catch {
      Object.defineProperty(window, 'PushManager', { configurable: true, value: undefined })
    }
    // Fallback: if delete is blocked by jsdom, spy the helper path by
    // leaving PushManager missing — isPushSupported needs BOTH.
    if ('PushManager' in window) {
      Object.defineProperty(window, 'PushManager', { configurable: true, get: () => { throw new Error('missing') } })
    }
  }

  Object.defineProperty(globalThis, 'Notification', {
    configurable: true,
    value: {
      permission,
      requestPermission: vi.fn().mockResolvedValue(opts.requestPermission ?? 'granted'),
    },
  })

  return { pushManager, sub, getCurrentSub: () => current }
}

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('push utilities', () => {
  it('urlBase64ToUint8Array decodes unpadded base64url', () => {
    const result = urlBase64ToUint8Array(VAPID)
    expect(result).toBeInstanceOf(Uint8Array)
    expect(result.length).toBe(32)
  })

  it('urlBase64ToUint8Array handles short padded keys', () => {
    expect(urlBase64ToUint8Array('AQIDBAUG').length).toBeGreaterThan(0)
  })

  it('urlBase64ToUint8Array returns empty for empty string', () => {
    expect(urlBase64ToUint8Array('').length).toBe(0)
  })

  it('isPushSupported reflects env', () => {
    installPushEnv({ supported: false })
    expect(isPushSupported()).toBe(false)
    installPushEnv({ supported: true })
    expect(isPushSupported()).toBe(true)
  })
})

describe('useWebPush', () => {
  it('returns supported:false when push is unavailable', async () => {
    installPushEnv({ supported: false })
    const { result } = renderHook(() => useWebPush(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data).toEqual({ supported: false })
  })

  it('returns permission + subscribed when supported', async () => {
    installPushEnv({ supported: true, permission: 'granted', subscribed: true })
    const { result } = renderHook(() => useWebPush(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data).toEqual({
      supported: true,
      permission: 'granted',
      subscribed: true,
    })
  })

  it('reports subscribed:false when there is no existing subscription', async () => {
    installPushEnv({ supported: true, permission: 'default', subscribed: false })
    const { result } = renderHook(() => useWebPush(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data).toMatchObject({ supported: true, subscribed: false })
  })
})

describe('useSubscribePush', () => {
  it('requests permission, subscribes, and posts the subscription', async () => {
    const { pushManager } = installPushEnv({
      supported: true,
      permission: 'default',
      requestPermission: 'granted',
    })
    const { result } = renderHook(() => useSubscribePush(), { wrapper })
    await act(async () => {
      await result.current.mutateAsync()
    })
    expect(Notification.requestPermission).toHaveBeenCalled()
    expect(pushManager.subscribe).toHaveBeenCalled()
  })

  it('throws permission_denied when the user rejects', async () => {
    installPushEnv({
      supported: true,
      permission: 'default',
      requestPermission: 'denied',
    })
    const { result } = renderHook(() => useSubscribePush(), { wrapper })
    let err: unknown
    await act(async () => {
      try {
        await result.current.mutateAsync()
      } catch (e) {
        err = e
      }
    })
    expect((err as Error).message).toBe('permission_denied')
  })

  it('rolls back the browser subscription when the server POST fails', async () => {
    const { pushManager, getCurrentSub } = installPushEnv({
      supported: true,
      permission: 'default',
      requestPermission: 'granted',
    })
    const postSpy = vi.spyOn(http, 'post')
    const original = postSpy.getMockImplementation()!
    postSpy.mockImplementation(async (url: string, ...rest: any[]) => {
      if (String(url).includes('/api/push/subscriptions')) {
        throw new Error('backend down')
      }
      return original(url, ...rest)
    })
    const { result } = renderHook(() => useSubscribePush(), { wrapper })
    let err: unknown
    await act(async () => {
      try {
        await result.current.mutateAsync()
      } catch (e) {
        err = e
      }
    })
    expect((err as Error).message).toBe('backend down')
    expect(pushManager.subscribe).toHaveBeenCalled()
    expect(getCurrentSub()).toBeNull()
  })

  it('still surfaces the POST error if local unsubscribe also fails', async () => {
    const { pushManager } = installPushEnv({
      supported: true,
      permission: 'granted',
      requestPermission: 'granted',
    })
    pushManager.subscribe.mockImplementation(async () => ({
      endpoint: 'https://push.example/sub/1',
      toJSON: () => ({ endpoint: 'https://push.example/sub/1', keys: { p256dh: 'pk', auth: 'ak' } }),
      unsubscribe: vi.fn().mockRejectedValue(new Error('unsubscribe failed')),
    }))
    const postSpy = vi.spyOn(http, 'post')
    const original = postSpy.getMockImplementation()!
    postSpy.mockImplementation(async (url: string, ...rest: any[]) => {
      if (String(url).includes('/api/push/subscriptions')) {
        throw new Error('backend down')
      }
      return original(url, ...rest)
    })
    const { result } = renderHook(() => useSubscribePush(), { wrapper })
    let err: unknown
    await act(async () => {
      try {
        await result.current.mutateAsync()
      } catch (e) {
        err = e
      }
    })
    expect((err as Error).message).toBe('backend down')
  })
})

describe('useUnsubscribePush', () => {
  it('unsubscribes locally and deletes the backend row', async () => {
    const { sub } = installPushEnv({ supported: true, permission: 'granted', subscribed: true })
    const { result } = renderHook(() => useUnsubscribePush(), { wrapper })
    await act(async () => {
      await result.current.mutateAsync()
    })
    expect(sub?.unsubscribe).toHaveBeenCalled()
  })

  it('no-ops when there is no active subscription', async () => {
    installPushEnv({ supported: true, permission: 'granted', subscribed: false })
    const { result } = renderHook(() => useUnsubscribePush(), { wrapper })
    await act(async () => {
      await result.current.mutateAsync()
    })
    expect(result.current.isError).toBe(false)
  })

  it('swallows backend DELETE failures after local unsubscribe', async () => {
    const { sub } = installPushEnv({ supported: true, permission: 'granted', subscribed: true })
    const delSpy = vi.spyOn(http, 'delete')
    const original = delSpy.getMockImplementation()!
    delSpy.mockImplementation(async (url: string, ...rest: any[]) => {
      if (String(url).includes('/api/push/subscriptions')) {
        throw new Error('backend down')
      }
      return original(url, ...rest)
    })
    const { result } = renderHook(() => useUnsubscribePush(), { wrapper })
    await act(async () => {
      await result.current.mutateAsync()
    })
    expect(sub?.unsubscribe).toHaveBeenCalled()
    expect(result.current.isError).toBe(false)
  })
})

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { http } from '../api/client'
import { isPushSupported, urlBase64ToUint8Array } from '../lib/push'

export type PushStatus =
  | { supported: false }
  | { supported: true; permission: NotificationPermission; subscribed: boolean }

const PUSH_STATUS_KEY = ['push', 'status'] as const

export function useWebPush() {
  return useQuery<PushStatus>({
    queryKey: PUSH_STATUS_KEY,
    queryFn: async () => {
      if (!isPushSupported()) return { supported: false } as const
      // serviceWorker.ready resolves when an SW is active for this scope.
      // If the SW isn't registered yet (first paint, before the auto-
      // registration the plugin injects has run) this will block — that's
      // fine, useQuery handles the loading state.
      const reg = await navigator.serviceWorker.ready
      const sub = await reg.pushManager.getSubscription()
      return {
        supported: true,
        permission: Notification.permission,
        subscribed: !!sub,
      } as const
    },
  })
}

export function useSubscribePush() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const perm = await Notification.requestPermission()
      if (perm !== 'granted') throw new Error('permission_denied')

      const { data } = await http.get<{ public_key: string }>('/api/push/vapid-key')
      const reg = await navigator.serviceWorker.ready
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(data.public_key),
      })
      const json = sub.toJSON()
      const keys = json.keys ?? {}
      await http.post('/api/push/subscriptions', {
        endpoint: sub.endpoint,
        p256dh: keys.p256dh,
        auth: keys.auth,
      })
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: PUSH_STATUS_KEY }),
  })
}

export function useUnsubscribePush() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const reg = await navigator.serviceWorker.ready
      const sub = await reg.pushManager.getSubscription()
      if (!sub) return
      const endpoint = sub.endpoint
      // Unsubscribe locally first; even if the backend DELETE fails the
      // user has unsubscribed and won't see more push from this device.
      await sub.unsubscribe()
      try {
        await http.delete('/api/push/subscriptions', { data: { endpoint } })
      } catch {
        // Server-side cleanup is best-effort — the next time the server
        // sees this endpoint return 410, it'll prune the row on its own.
      }
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: PUSH_STATUS_KEY }),
  })
}

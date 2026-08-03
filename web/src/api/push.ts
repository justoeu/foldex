import { http } from './client'

export type VapidKeyResponse = { public_key: string }

export type PushSubscriptionBody = {
  endpoint: string
  p256dh?: string
  auth?: string
}

export async function fetchVapidKey(): Promise<VapidKeyResponse> {
  const { data } = await http.get<VapidKeyResponse>('/api/push/vapid-key')
  return data
}

export async function createPushSubscription(body: PushSubscriptionBody): Promise<void> {
  await http.post('/api/push/subscriptions', body)
}

export async function deletePushSubscription(endpoint: string): Promise<void> {
  await http.delete('/api/push/subscriptions', { data: { endpoint } })
}

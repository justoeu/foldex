import { useQuery } from '@tanstack/react-query'
import { http } from './client'

export type DepResource = {
  id: string
  state: 'ok' | 'unreachable'
}

export type DepStatus = {
  resources: DepResource[]
}

const KNOWN_IDS = new Set(['object_store', 'mail_broker'])

export function fetchDepStatus(): Promise<DepStatus> {
  return http.get<DepStatus>('/api/status').then((r) => r.data)
}

export function unreachableResources(status: DepStatus | undefined): DepResource[] {
  return (status?.resources ?? []).filter(
    (r) => r.state === 'unreachable' && KNOWN_IDS.has(r.id),
  )
}

export function useDepStatus() {
  return useQuery({
    queryKey: ['status', 'deps'],
    queryFn: fetchDepStatus,
    staleTime: 15_000,
    refetchInterval: 30_000,
  })
}

import { createContext, createElement, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
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

export type ObjectStoreSighting = 'ok' | 'unreachable' | 'absent'

export function objectStoreSighting(status: DepStatus | undefined): ObjectStoreSighting {
  const row = status?.resources?.find((r) => r.id === 'object_store')
  if (row?.state === 'ok' || row?.state === 'unreachable') return row.state
  return 'absent'
}

export function shouldRetryObjectStoreImages(
  prev: ObjectStoreSighting,
  next: ObjectStoreSighting,
): boolean {
  return prev === 'unreachable' && next === 'ok'
}

const ObjectStoreGenerationContext = createContext(0)

// Bumps when the object store returns so cards remount <img> (INV-082 hid
// the broken file) and /api/files URLs get a cache-buster the browser will
// actually fetch again. Default 0 when no provider (FolderCard unit tests).
export function useObjectStoreGeneration(): number {
  return useContext(ObjectStoreGenerationContext)
}

export function ObjectStoreGenerationProvider({ children }: Readonly<{ children: ReactNode }>) {
  const { data } = useDepStatus()
  const [generation, setGeneration] = useState(0)
  const prev = useRef<ObjectStoreSighting>('absent')
  const next = objectStoreSighting(data)
  useEffect(() => {
    if (shouldRetryObjectStoreImages(prev.current, next)) {
      setGeneration((n) => n + 1)
    }
    prev.current = next
  }, [next])
  return createElement(ObjectStoreGenerationContext.Provider, { value: generation }, children)
}

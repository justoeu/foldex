import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { lookupLinkByURL } from '../api/links'
import { looksLikeUrl } from '../lib/url'
import type { Link } from '../api/types'

const LOOKUP_DEBOUNCE_MS = 500

/** Owner-scoped uniqueness probe for the new-link dialog (INV-054). */
export function useExistingLinkByURL(
  url: string,
  enabled: boolean,
  excludeId?: number | null,
): { existing: Link | null; pending: boolean } {
  const trimmed = url.trim()
  const [debounced, setDebounced] = useState('')

  useEffect(() => {
    if (!enabled) {
      setDebounced('')
      return
    }
    const timer = window.setTimeout(() => setDebounced(trimmed), LOOKUP_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [trimmed, enabled])

  const query = useQuery({
    queryKey: ['links', 'by-url', debounced],
    queryFn: () => lookupLinkByURL(debounced),
    enabled: enabled && looksLikeUrl(debounced),
    retry: false,
    staleTime: 0,
  })

  const hit = query.data ?? null
  if (hit && excludeId != null && hit.id === excludeId) {
    return { existing: null, pending: false }
  }
  return { existing: hit, pending: query.isFetching }
}

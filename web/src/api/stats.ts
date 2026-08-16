import { useQuery } from '@tanstack/react-query'
import { http } from './client'

export type StatsSummary = {
  total_links: number
  total_tags: number
  total_clicks: number
  clicks_last_30d: number
  clicks_prev_30d: number
  new_links_last_30d: number
  top_host: string
  top_host_clicks: number
}

export type DailyPoint = {
  date: string
  clicks: number
}

export type TopLink = {
  id: number
  url: string
  title: string
  slug: string
  host: string
  clicks: number
  clicks_30d: number
  clicks_prev_30d: number
}

export type TagBucket = {
  id: number
  name: string
  color: string
  clicks: number
  links: number
}

export type StatsDashboard = {
  summary: StatsSummary
  daily: DailyPoint[]
  top: TopLink[]
  tags: TagBucket[]
}

export function useStatsDashboard(days = 60, limit = 5) {
  return useQuery({
    queryKey: ['stats', 'dashboard', days, limit],
    queryFn: async () => (await http.get<StatsDashboard>(`/api/stats/dashboard?days=${days}&limit=${limit}`)).data,
  })
}

export type StorageStats = {
  objects: number
  total_bytes: number
}

export function useStatsStorage() {
  return useQuery({
    queryKey: ['stats', 'storage'],
    queryFn: async () => {
      try {
        return (await http.get<StorageStats>('/api/stats/storage')).data
      } catch {
        // Endpoint is absent when the object store is unreachable — render the card as
        // "—" rather than crashing the whole page.
        return null
      }
    },
  })
}

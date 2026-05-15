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

export function useStatsSummary() {
  return useQuery({
    queryKey: ['stats', 'summary'],
    queryFn: async () => (await http.get<StatsSummary>('/api/stats/summary')).data,
  })
}

export function useStatsDaily(days = 60) {
  return useQuery({
    queryKey: ['stats', 'daily', days],
    queryFn: async () => (await http.get<DailyPoint[]>(`/api/stats/daily?days=${days}`)).data,
  })
}

export function useStatsTop(limit = 5) {
  return useQuery({
    queryKey: ['stats', 'top', limit],
    queryFn: async () => (await http.get<TopLink[]>(`/api/stats/top?limit=${limit}`)).data,
  })
}

export function useStatsTags() {
  return useQuery({
    queryKey: ['stats', 'tags'],
    queryFn: async () => (await http.get<TagBucket[]>('/api/stats/tags')).data,
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
        // Endpoint is absent when MinIO is unreachable — render the card as
        // "—" rather than crashing the whole page.
        return null
      }
    },
  })
}

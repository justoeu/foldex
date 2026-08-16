import { useQuery } from '@tanstack/react-query'
import { entryCountsKey, fetchEntryCounts } from '../api/entries'

export function useEntryCounts() {
  return useQuery({
    queryKey: entryCountsKey,
    queryFn: fetchEntryCounts,
  })
}

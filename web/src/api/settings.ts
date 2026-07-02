import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { http } from './client'

// Master recovery password (ADR-29). The API never returns the hash — only
// whether one is configured, plus the non-secret reminder hint (nil when unset).
export type MasterPasswordStatus = { configured: boolean; hint?: string | null }

export function useMasterPasswordStatus() {
  return useQuery({
    queryKey: ['settings', 'master-password'],
    queryFn: async () => {
      const { data } = await http.get<MasterPasswordStatus>('/api/settings/master-password')
      return data
    },
    staleTime: 60_000,
  })
}

// Set or change the master password. current_password is required by the
// backend only when one is already configured (changing it).
export function useSetMasterPassword() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({
      password,
      currentPassword,
      hint,
    }: {
      password: string
      currentPassword?: string
      hint?: string | null
    }) => {
      const { data } = await http.put<MasterPasswordStatus>('/api/settings/master-password', {
        password,
        current_password: currentPassword,
        hint,
      })
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings', 'master-password'] }),
  })
}

export function useRemoveMasterPassword() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ currentPassword }: { currentPassword: string }) => {
      const { data } = await http.delete<MasterPasswordStatus>('/api/settings/master-password', {
        data: { current_password: currentPassword },
      })
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings', 'master-password'] }),
  })
}

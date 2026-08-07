import { http } from './client'

/**
 * A long-lived bearer credential for the browser extension and for scripts.
 *
 * `token` is present ONLY in the response that creates one. The server stores
 * sha256, so showing it again is not a feature that was left out — it is one
 * that cannot exist.
 */
export type ApiToken = {
  id: number
  name: string
  scope: string
  created_at: string
  last_used_at?: string
  expires_at?: string
  token?: string
}

export async function listTokens(): Promise<ApiToken[]> {
  const { data } = await http.get<{ tokens: ApiToken[] }>('/api/auth/tokens')
  return data.tokens
}

export async function createToken(name: string, expiresInDays = 0): Promise<ApiToken> {
  const { data } = await http.post<ApiToken>('/api/auth/tokens', {
    name,
    expires_in_days: expiresInDays,
  })
  return data
}

export async function revokeToken(id: number): Promise<void> {
  await http.delete(`/api/auth/tokens/${id}`)
}

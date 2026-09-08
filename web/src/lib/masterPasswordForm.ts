import { MIN_PASSWORD_LEN } from '../auth/types'
import { hintEqualsPassword } from './hintEqualsPassword'

export type MasterFormInput = {
  next: string
  confirm: string
  hint: string
  configured: boolean
  current: string
}

export type MasterFormPayload = {
  password: string
  currentPassword?: string
  hint?: string
}

export type MasterFormResult =
  | { ok: true; payload: MasterFormPayload }
  | { ok: false; errorKey: string }

export function validateMasterForm(input: MasterFormInput): MasterFormResult {
  if (input.next.length < MIN_PASSWORD_LEN) {
    return { ok: false, errorKey: 'settings.master_too_short' }
  }
  if (new TextEncoder().encode(input.next).length > 72) {
    return { ok: false, errorKey: 'settings.master_too_long' }
  }
  if (input.next !== input.confirm) {
    return { ok: false, errorKey: 'settings.master_mismatch' }
  }
  const trimmedHint = input.hint.trim()
  if (hintEqualsPassword(trimmedHint, input.next)) {
    return { ok: false, errorKey: 'settings.master_hint_equals' }
  }
  if (input.configured && !input.current) {
    return { ok: false, errorKey: 'settings.master_wrong_current' }
  }
  const payload: MasterFormPayload = { password: input.next }
  if (input.configured) payload.currentPassword = input.current
  if (trimmedHint) payload.hint = trimmedHint
  return { ok: true, payload }
}

export function validateMasterRemove(current: string):
  | { ok: true; payload: { currentPassword: string } }
  | { ok: false; errorKey: string } {
  if (!current) return { ok: false, errorKey: 'settings.master_wrong_current' }
  return { ok: true, payload: { currentPassword: current } }
}

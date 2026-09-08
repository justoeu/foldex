import { describe, expect, it } from 'vitest'
import authSrc from './auth.ts?raw'

describe('auth error envelope', () => {
  it('does not reimplement errorCode or errorStatus', () => {
    expect(authSrc).not.toMatch(/export function errorCode/)
    expect(authSrc).not.toMatch(/export function errorStatus/)
  })
})

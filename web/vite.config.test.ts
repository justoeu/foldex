import { afterEach, describe, expect, it, vi } from 'vitest'

import viteConfig from './vite.config'

async function resolveViteConfig(lanOptIn: string) {
  vi.stubEnv('VITE_DEV_LAN', lanOptIn)
  if (typeof viteConfig !== 'function') throw new Error('expected functional Vite config')
  return await viteConfig({
    command: 'serve',
    mode: 'test',
    isSsrBuild: false,
    isPreview: false,
  })
}

describe('Vite development bind', () => {
  afterEach(() => vi.unstubAllEnvs())

  it('binds to loopback by default', async () => {
    const config = await resolveViteConfig('')
    expect(config.server?.host).toBe('127.0.0.1')
  })

  it('binds to all interfaces only after an explicit LAN opt-in', async () => {
    const config = await resolveViteConfig('1')
    expect(config.server?.host).toBe('0.0.0.0')
  })
})

describe('Vite backend proxy', () => {
  afterEach(() => vi.unstubAllEnvs())

  it('matches backend routes without capturing dependency imports', async () => {
    const config = await resolveViteConfig('')
    const patterns = Object.keys(config.server?.proxy ?? {}).map((pattern) => new RegExp(pattern))

    expect(patterns).toHaveLength(1)
    expect(patterns[0].test('/api/auth/me')).toBe(true)
    expect(patterns[0].test('/go/example')).toBe(true)
    expect(patterns[0].test('/n/example')).toBe(true)
    expect(patterns[0].test('/node_modules/.vite/deps/react.js')).toBe(false)
  })
})

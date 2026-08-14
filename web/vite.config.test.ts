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

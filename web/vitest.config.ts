/// <reference types="vitest" />
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/storage.ts', './src/test/setup.ts'],
    css: false,
    // Vitest defaults to 5s. That is comfortable for the plain run (~30ms per
    // test) but not under v8 coverage instrumentation, which multiplies the
    // wall clock of a jsdom render by roughly five — enough for the heavier
    // dialog tests to trip the default and fail as timeouts that say nothing
    // about the code. The gate is coverage, not speed, so the ceiling is only
    // here to catch a genuine hang.
    testTimeout: 20_000,
    hookTimeout: 20_000,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json-summary', 'html'],
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/main.tsx',
        'src/**/*.test.{ts,tsx}',
        'src/test/**',
        'src/theme/**',
      ],
      thresholds: {
        lines: 85,
        statements: 85,
        functions: 85,
        branches: 80,
      },
    },
  },
})

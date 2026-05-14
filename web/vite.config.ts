import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  const target = env.VITE_API_BASE || 'http://localhost:9089'
  return {
    plugins: [react()],
    server: {
      host: '0.0.0.0',
      port: 9088,
      proxy: {
        '/api': { target, changeOrigin: true },
        '/go':  { target, changeOrigin: true },
      },
    },
    build: {
      // Sourcemaps in prod cost ~30-40% of bundle size, but for a self-hosted
      // single-user app the trade-off is worth it: ErrorBoundary stack traces
      // map back to real source lines (React #300 et al become actionable).
      sourcemap: true,
      rolldownOptions: {
        output: {
          manualChunks(id: string) {
            if (!id.includes('node_modules')) return
            if (id.includes('@mui') || id.includes('@emotion')) return 'vendor-mui'
            if (id.includes('@tanstack') || id.includes('/axios/')) return 'vendor-query'
            if (id.includes('/react')) return 'vendor-react'
          },
        },
      },
    },
  }
})

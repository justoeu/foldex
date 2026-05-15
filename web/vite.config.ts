import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import { VitePWA } from 'vite-plugin-pwa'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  const target = env.VITE_API_BASE || 'http://localhost:9089'
  return {
    plugins: [
      react(),
      VitePWA({
        // Generate the SW + manifest. The previously hand-written
        // public/manifest.webmanifest is replaced by this config so the
        // manifest stays in sync with the actual build output (revisioned
        // asset URLs in the precache match the SW's `precacheAndRoute`).
        registerType: 'autoUpdate',
        injectRegister: 'auto',
        manifestFilename: 'manifest.webmanifest',
        manifest: {
          name: 'Foldex',
          short_name: 'Foldex',
          description:
            'Self-hosted bookmark manager with rich tagging, nestable folders, click tracking and visual URL previews.',
          start_url: '/',
          scope: '/',
          display: 'standalone',
          orientation: 'any',
          background_color: '#F5F4FB',
          theme_color: '#6366F1',
          lang: 'en',
          categories: ['productivity', 'utilities'],
          prefer_related_applications: false,
          icons: [
            { src: '/favicon.svg', sizes: 'any', type: 'image/svg+xml', purpose: 'any' },
            { src: '/favicon.svg', sizes: 'any', type: 'image/svg+xml', purpose: 'maskable' },
          ],
        },
        workbox: {
          // Precache every built asset Vite produced. Revisioned URLs mean
          // stale caches roll forward automatically on next page load.
          globPatterns: ['**/*.{js,css,html,svg,png,webp,jpg,jpeg,gif,woff2}'],
          // Don't precache the build's source maps — they're large and
          // useless to most users.
          globIgnores: ['**/*.map'],
          // Navigation requests fall back to index.html when offline so
          // the SPA shell still mounts (router takes over from there).
          navigateFallback: '/index.html',
          // The API and the short-link redirect are NOT cacheable — they
          // mutate state on click and must always hit the backend.
          navigateFallbackDenylist: [/^\/api\//, /^\/go\//, /^\/healthz/],
          runtimeCaching: [
            {
              // Favicons / og:images we proxy from /api/files/. Network-
              // first so a refreshed image lands on the next view, with a
              // 30-day cache fallback for offline.
              urlPattern: /^https?:\/\/[^/]+\/api\/files\//,
              handler: 'NetworkFirst',
              options: {
                cacheName: 'foldex-files',
                expiration: { maxEntries: 200, maxAgeSeconds: 30 * 24 * 60 * 60 },
                cacheableResponse: { statuses: [0, 200] },
              },
            },
          ],
        },
        devOptions: {
          // Disable in `vite dev` — the SW caches stale chunks and makes
          // HMR feel broken. Production builds get the full PWA.
          enabled: false,
        },
      }),
    ],
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

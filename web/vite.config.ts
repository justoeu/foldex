import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import { VitePWA } from 'vite-plugin-pwa'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  const target = env.VITE_API_BASE || 'http://localhost:9089'
  // Stamp the build date so the sidebar footer can show "v1.2.3 · 2026-05-15"
  // without needing a pre-build script that mutates a source file. The
  // VERSION itself comes from web/package.json (bumped by
  // `make release-{patch,minor,major}` — see web/src/version.ts).
  const buildDate = new Date().toISOString().slice(0, 10)
  return {
    define: {
      __FOLDEX_BUILD_DATE__: JSON.stringify(buildDate),
    },
    plugins: [
      react(),
      VitePWA({
        // injectManifest hands us full control of the SW (web/src/sw.ts).
        // Required because the SW now handles Web Push (`push` +
        // `notificationclick` listeners) on top of the precache + runtime
        // strategies — generateSW can't express custom event handlers.
        strategies: 'injectManifest',
        srcDir: 'src',
        filename: 'sw.ts',
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
        // Inject the precache manifest into web/src/sw.ts. The runtime
        // caching strategy and push handlers live in sw.ts directly — see
        // its top-of-file rationale for why we don't depend on workbox-*
        // runtime packages.
        injectManifest: {
          globPatterns: ['**/*.{js,css,html,svg,png,webp,jpg,jpeg,gif,woff2}'],
          globIgnores: ['**/*.map'],
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
        '/n':   { target, changeOrigin: true },
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

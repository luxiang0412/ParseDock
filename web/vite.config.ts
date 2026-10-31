import type { ProxyOptions } from 'vite'
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

/**
 * The browser never holds the API key. In dev and in `vite preview` this proxy injects it
 * from the *Node* process environment (`PARSEDOCK_DEV_KEY`), which is never inlined into the
 * bundle (only `VITE_*` variables are). Production does the same in nginx
 * (`deploy/nginx.playground.conf.template`).
 */
const upstream = process.env.PARSEDOCK_API_UPSTREAM ?? 'http://127.0.0.1:8080'
const key = process.env.PARSEDOCK_DEV_KEY ?? ''

const proxy: Record<string, ProxyOptions> = {
  // The UI's health dot asks /healthz; it must reach the API, not the static server.
  '/healthz': { target: upstream, changeOrigin: true },
  '/readyz': { target: upstream, changeOrigin: true },
  '/v1': {
    target: upstream,
    changeOrigin: true,
    // SSE must not be buffered; the backend already sends X-Accel-Buffering: no.
    configure: (p) => {
      p.on('proxyReq', (req) => {
        req.removeHeader('cookie')
        // The gateway also accepts X-Api-Key; a page must never pick its own key.
        req.removeHeader('x-api-key')
        if (key) req.setHeader('authorization', `Bearer ${key}`)
      })
    },
  },
}

export default defineConfig({
  // No client-side router: unknown paths must 404 instead of returning the SPA shell,
  // so a request to /internal/* can never be answered by the app.
  appType: 'mpa',
  plugins: [react(), tailwindcss()],
  server: { port: 5173, proxy },
  preview: { port: 4173, proxy },
  build: {
    target: 'es2022',
    sourcemap: false,
    rollupOptions: {
      output: {
        // Office renderers are large and only needed after a result exists.
        manualChunks: {
          docx: ['docx-preview'],
          xlsx: ['exceljs'],
        },
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./vitest.setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    css: false,
  },
})

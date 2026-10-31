import { resolve } from 'node:path'
import { defineConfig, devices } from '@playwright/test'

/**
 * E2E runs against a real ParseDock backend, not a mock: the Go binary serves the API on
 * 18080 and `vite preview` serves the built app with the same-origin `/v1` proxy.
 *
 * `E2E_ENGINES` selects what the backend can parse:
 *   - "mock,native"  → fast PR job (text formats + a deterministic mock engine)
 *   - "native,xberg" → a real PDF (nightly, where the xberg binaries are installed)
 */
const engines = process.env.E2E_ENGINES ?? 'mock,native'
const apiPort = 18080
const webPort = 4173
const key = process.env.E2E_KEY ?? 'e2e-key'
/**
 * Against the Docker Compose stack (`npm run e2e:compose`) the servers already exist:
 * E2E_COMPOSE_URL=http://127.0.0.1:8090 and no webServer entries are started.
 */
const composeUrl = process.env.E2E_COMPOSE_URL
const repo = resolve(process.cwd(), '..')
const abs = (p: string) => resolve(repo, p)
const bin = process.env.E2E_BINARY ?? abs('runtime/bin/docparse')

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  timeout: 120_000,
  expect: { timeout: 15_000 },
  use: {
    baseURL: composeUrl ?? `http://127.0.0.1:${webPort}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'off',
  },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } } },
    { name: 'mobile', use: { ...devices['Pixel 7'] } },
  ],
  webServer: composeUrl ? undefined : [
    {
      command: [
        bin,
        '--role', 'standalone',
        '--listen', `127.0.0.1:${apiPort}`,
        '--internal-listen', `127.0.0.1:${apiPort + 1}`,
        '--data-dir', abs('runtime/e2e-playground/data'),
        '--work-dir', abs('runtime/e2e-playground/work'),
        '--engines', engines,
        '--api-keys', `${key}:e2e-owner`,
        '--max-upload-mb', '128',
        '--max-pages', '2000',
        '--result-ttl', '45s',
        '--cleanup-interval', '2s',
        '--worker-concurrency', '2',
        '--xberg-bin', abs('runtime/engines/xberg-cli/xberg'),
        '--xberg-lib-dir', abs('runtime/engines/xberg-cli'),
      ].join(' '),
      url: `http://127.0.0.1:${apiPort}/healthz`,
      reuseExistingServer: false,
      timeout: 60_000,
      env: { PARSEDOCK_MOCK_DELAY_MS: '150' },
    },
    {
      command: `npm run preview -- --port ${webPort} --strictPort`,
      url: `http://127.0.0.1:${webPort}`,
      reuseExistingServer: false,
      timeout: 60_000,
      env: {
        PARSEDOCK_API_UPSTREAM: `http://127.0.0.1:${apiPort}`,
        PARSEDOCK_DEV_KEY: key,
      },
    },
  ],
})

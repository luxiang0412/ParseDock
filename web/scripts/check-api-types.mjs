#!/usr/bin/env node
/**
 * The committed `src/api/schema.d.ts` must match api/openapi.yaml. Regenerating it in CI and
 * diffing catches a backend contract change that the UI has not seen.
 */
import { execFileSync } from 'node:child_process'
import { readFileSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const tmp = mkdtempSync(join(tmpdir(), 'pd-types-'))
try {
  const out = join(tmp, 'schema.d.ts')
  execFileSync('npx', ['--no-install', 'openapi-typescript', '../api/openapi.yaml', '-o', out], {
    stdio: 'inherit',
  })
  const fresh = readFileSync(out, 'utf8')
  const committed = readFileSync('src/api/schema.d.ts', 'utf8')
  if (fresh !== committed) {
    console.error('src/api/schema.d.ts is stale: run `npm run api:types` and commit the result.')
    process.exit(1)
  }
  console.log('api types are up to date')
} finally {
  rmSync(tmp, { recursive: true, force: true })
}

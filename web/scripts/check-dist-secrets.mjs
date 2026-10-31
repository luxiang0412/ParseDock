#!/usr/bin/env node
/**
 * Fails if anything that looks like the proxy's API key ended up in the built bundle. The key
 * is only ever read by the dev/preview proxy and by nginx, never by the browser.
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

const dist = 'dist'
const needles = [process.env.PARSEDOCK_DEV_KEY, process.env.PARSEDOCK_API_TOKEN, 'PARSEDOCK_DEV_KEY'].filter(
  (v) => typeof v === 'string' && v.length >= 8,
)

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}

let bad = 0
for (const file of walk(dist)) {
  const text = readFileSync(file, 'utf8')
  for (const needle of needles) {
    if (text.includes(needle)) {
      console.error(`secret found in ${file}`)
      bad += 1
    }
  }
  for (const marker of ['X-Internal-Token', 'PARSEDOCK_INTERNAL_TOKEN']) {
    if (text.includes(marker)) {
      console.error(`internal marker ${marker} found in ${file}`)
      bad += 1
    }
  }
}
if (bad > 0) process.exit(1)
console.log(`no secrets in ${dist}`)

import type { components } from './schema'
import { toApiError } from './errors'

export type Job = components['schemas']['Job']
export type JobStatus = Job['status']
export type RetentionStatus = Job['retention_status']
export type Mode = 'auto' | 'fast' | 'accurate'
export type DocumentIR = components['schemas']['DocumentIR']

const BASE = '/v1'

/** The server stamps the ACK token on this header; it is not the ETag. */
export const RESULT_VERSION_HEADER = 'x-result-version'

export interface UploadInput {
  file: File
  mode: Mode
  signal?: AbortSignal
}

export interface ResultPayload {
  markdown: string
  /** `X-Result-Version` — the only valid ACK token. */
  resultVersion: string | null
}

/**
 * Every request carries this marker. The production proxy rejects state-changing requests
 * without it, which a cross-site form cannot set (CSRF defence around the shared key).
 */
const PLAYGROUND_HEADER = 'X-Parsedock-Playground'

async function request(path: string, init: RequestInit = {}): Promise<Response> {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    credentials: 'same-origin',
    headers: { ...(init.headers as Record<string, string> | undefined), [PLAYGROUND_HEADER]: '1' },
  })
  if (!res.ok) throw await toApiError(res)
  return res
}

export const api = {
  async createJob({ file, mode, signal }: UploadInput): Promise<Job> {
    const body = new FormData()
    body.append('file', file, file.name)
    body.append('mode', mode)
    const res = await request('/jobs', { method: 'POST', body, signal })
    return (await res.json()) as Job
  },

  async getJob(id: string, signal?: AbortSignal): Promise<Job> {
    const res = await request(`/jobs/${encodeURIComponent(id)}`, { signal })
    return (await res.json()) as Job
  },

  async getMarkdown(id: string, signal?: AbortSignal): Promise<ResultPayload> {
    const res = await request(`/jobs/${encodeURIComponent(id)}/result?format=markdown`, { signal })
    return { markdown: await res.text(), resultVersion: res.headers.get(RESULT_VERSION_HEADER) }
  },

  /**
   * Optional metadata (engine metrics, warning codes). Capped on purpose: a real workbook
   * renders to a 19 MB IR, and the UI only needs a few scalars from it.
   */
  async getIR(id: string, maxBytes = 4 << 20, signal?: AbortSignal): Promise<DocumentIR | null> {
    const res = await request(`/jobs/${encodeURIComponent(id)}/result?format=json`, { signal })
    const reader = res.body?.getReader()
    if (!reader) return null
    const chunks: Uint8Array[] = []
    let size = 0
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        if (!value) continue
        size += value.byteLength
        if (size > maxBytes) {
          await reader.cancel()
          return null
        }
        chunks.push(value)
      }
    } catch {
      return null
    }
    const text = new TextDecoder().decode(concat(chunks, size))
    try {
      return JSON.parse(text) as DocumentIR
    } catch {
      return null
    }
  },

  /** Explicit user action only — never called automatically. */
  async ack(id: string, resultVersion: string): Promise<Job> {
    const res = await request(`/jobs/${encodeURIComponent(id)}/ack`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Result-Version': resultVersion },
      body: JSON.stringify({ result_version: resultVersion }),
    })
    return (await res.json()) as Job
  },

  async cancel(id: string): Promise<Job> {
    const res = await request(`/jobs/${encodeURIComponent(id)}/cancel`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    })
    return (await res.json()) as Job
  },

  async health(): Promise<boolean> {
    try {
      const res = await fetch('/healthz', { credentials: 'same-origin' })
      return res.ok
    } catch {
      return false
    }
  },
}

function concat(chunks: Uint8Array[], size: number): Uint8Array {
  const out = new Uint8Array(size)
  let at = 0
  for (const c of chunks) {
    out.set(c, at)
    at += c.byteLength
  }
  return out
}

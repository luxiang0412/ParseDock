/** Stable error codes from api/openapi.yaml; mapped to a sentence the user can act on. */
const MESSAGES: Record<string, string> = {
  invalid_request: 'The request was rejected as invalid. Check the file and try again.',
  unsupported_type: 'This file type is not accepted by this deployment.',
  malformed_file: 'The file looks damaged or is not what its extension claims.',
  file_too_large: 'The file is larger than the upload limit of this deployment.',
  encrypted_file: 'The document is password protected, so it cannot be parsed.',
  page_limit_exceeded: 'The document has more pages than this deployment allows.',
  suspicious_compression: 'The archive expands suspiciously far; it was rejected for safety.',
  expanded_too_large: 'The document expands beyond the parser limits (large spreadsheets).',
  quota_exceeded: 'This owner has reached a quota (concurrent jobs, daily jobs or storage).',
  insufficient_storage: 'The server is low on disk space. Try again later.',
  engine_unavailable: 'No parser engine is available for this document right now.',
  gpu_unavailable: 'A GPU worker was requested but none is available with enough VRAM.',
  timeout: 'Parsing took longer than the limit for this document.',
  cancelled: 'The job was cancelled.',
  internal_error: 'The server hit an internal error while parsing.',
  lease_lost: 'The worker processing this job was lost; the job was retried.',
  not_found: 'That job does not exist for this API key.',
  gone: 'The result was released or expired and can no longer be downloaded.',
  conflict: 'The job is not in a state where that action is allowed.',
  unauthorized: 'The API key is missing or invalid.',
  insufficient_scope: 'This API key does not grant the required scope.',
  rate_limited: 'Too many requests; wait a moment and retry.',
  coordinator_unavailable: 'The coordinator is unavailable (the API replica cannot reach it).',
}

export function messageForCode(code: string | undefined, fallback?: string): string {
  if (!code) return fallback ?? 'Something went wrong.'
  return MESSAGES[code] ?? fallback ?? `Unexpected error (${code}).`
}

/** A typed view over the `{"error":{"code","message"}}` envelope. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly retryAfter?: number,
  ) {
    super(message)
    this.name = 'ApiError'
  }

  /** The sentence to show in the UI (keeps the server's detail for the small print). */
  get friendly(): string {
    if (this.code === 'rate_limited' && this.retryAfter) {
      return `Too many requests; retry in about ${this.retryAfter} s.`
    }
    return messageForCode(this.code, this.message)
  }
}

export async function toApiError(res: Response): Promise<ApiError> {
  const retryAfter = Number(res.headers.get('retry-after') ?? '') || undefined
  let code = `http_${res.status}`
  let message = res.statusText || 'request failed'
  try {
    const body = (await res.json()) as { error?: { code?: string; message?: string } }
    if (body.error?.code) code = body.error.code
    if (body.error?.message) message = body.error.message
  } catch {
    /* not JSON: keep the status text */
  }
  return new ApiError(res.status, code, message, retryAfter)
}

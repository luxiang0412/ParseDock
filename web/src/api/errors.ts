import type { MessageKey } from '../i18n/messages'
import type { Translator } from '../features/playground/format'
import { MESSAGES } from '../i18n/messages'

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

  /** The sentence to show in the UI, in the caller's locale. */
  friendly(t: Translator): string {
    return friendlyForCode(this.code, t, this.message, this.retryAfter)
  }
}

/** Maps a stable API error code to a sentence; unknown codes keep the server's own message. */
export function friendlyForCode(
  code: string | undefined,
  t: Translator,
  detail?: string,
  retryAfter?: number,
): string {
  if (!code) return detail || t('error.unknown')
  if (code === 'rate_limited' && retryAfter) {
    return t('error.rate_limited_after', { seconds: retryAfter })
  }
  const key = `error.${code}` as MessageKey
  if (key in MESSAGES.en) return t(key)
  // Unknown code: the server's own message is more useful than a generic sentence.
  if (detail) return detail
  return t('error.unexpected', { code })
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

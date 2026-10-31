import { describe, expect, it } from 'vitest'
import { ApiError, friendlyForCode, toApiError } from './errors'
import { translate } from '../i18n'

const en: Parameters<typeof friendlyForCode>[1] = (key, vars) => translate('en', key, vars)
const zh: Parameters<typeof friendlyForCode>[1] = (key, vars) => translate('zh-CN', key, vars)

describe('friendlyForCode', () => {
  it('maps known codes to actionable sentences', () => {
    expect(friendlyForCode('unsupported_type', en)).toMatch(/not accepted/i)
    expect(friendlyForCode('encrypted_file', en)).toMatch(/password protected/i)
    expect(friendlyForCode('gone', en)).toMatch(/released or expired/i)
  })
  it('speaks Chinese when asked', () => {
    expect(friendlyForCode('file_too_large', zh)).toMatch(/上传上限/)
    expect(friendlyForCode('rate_limited', zh, undefined, 7)).toContain('7')
  })
  it('falls back to the server message for unknown codes', () => {
    expect(friendlyForCode('http_502', en, 'bad gateway')).toBe('bad gateway')
    expect(friendlyForCode(undefined, en)).toMatch(/went wrong/i)
  })
})

describe('toApiError', () => {
  it('reads the error envelope and Retry-After', async () => {
    const res = new Response(
      JSON.stringify({ error: { code: 'rate_limited', message: 'too many requests' } }),
      { status: 429, headers: { 'retry-after': '7' } },
    )
    const err = await toApiError(res)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.code).toBe('rate_limited')
    expect(err.status).toBe(429)
    expect(err.retryAfter).toBe(7)
    expect(err.friendly(en)).toMatch(/7 s/)
  })

  it('survives a non-JSON body', async () => {
    const err = await toApiError(new Response('<html>oops</html>', { status: 502 }))
    expect(err.code).toBe('http_502')
    expect(err.status).toBe(502)
  })
})

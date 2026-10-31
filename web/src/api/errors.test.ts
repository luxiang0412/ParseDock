import { describe, expect, it } from 'vitest'
import { ApiError, messageForCode, toApiError } from './errors'

describe('messageForCode', () => {
  it('maps known codes to actionable sentences', () => {
    expect(messageForCode('unsupported_type')).toMatch(/not accepted/i)
    expect(messageForCode('encrypted_file')).toMatch(/password protected/i)
    expect(messageForCode('gone')).toMatch(/released or expired/i)
  })
  it('falls back to the server message for unknown codes', () => {
    expect(messageForCode('http_502', 'bad gateway')).toBe('bad gateway')
    expect(messageForCode(undefined)).toMatch(/went wrong/i)
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
    expect(err.friendly).toMatch(/7 s/)
  })

  it('survives a non-JSON body', async () => {
    const err = await toApiError(new Response('<html>oops</html>', { status: 502 }))
    expect(err.code).toBe('http_502')
    expect(err.status).toBe(502)
  })
})

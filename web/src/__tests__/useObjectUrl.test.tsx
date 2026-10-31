import { renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useObjectUrl } from '../hooks/useObjectUrl'

describe('useObjectUrl', () => {
  let created: string[]
  let revoked: string[]

  beforeEach(() => {
    created = []
    revoked = []
    let n = 0
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL: (b: Blob) => {
        const url = `blob:test/${++n}`
        created.push(`${url}:${b.size}`)
        return url
      },
      revokeObjectURL: (url: string) => void revoked.push(url),
    })
  })

  it('creates one URL per blob and revokes it on change and unmount', () => {
    const a = new Blob(['aaa'])
    const b = new Blob(['bb'])
    const { result, rerender, unmount } = renderHook(({ blob }: { blob: Blob }) => useObjectUrl(blob), {
      initialProps: { blob: a },
    })
    expect(result.current).toBe('blob:test/1')
    expect(created).toEqual(['blob:test/1:3'])

    rerender({ blob: b })
    expect(result.current).toBe('blob:test/2')
    expect(revoked).toEqual(['blob:test/1'])

    rerender({ blob: b }) // same blob: no new URL
    expect(created).toHaveLength(2)

    unmount()
    expect(revoked).toEqual(['blob:test/1', 'blob:test/2'])
  })

  it('returns null without a blob', () => {
    const { result } = renderHook(() => useObjectUrl(null))
    expect(result.current).toBeNull()
  })
})

import { describe, expect, it } from 'vitest'
import { decodeTextFile } from './text'

function fileFrom(bytes: Uint8Array, name: string): File {
  return new File([new Uint8Array(bytes)], name)
}

describe('decodeTextFile', () => {
  it('decodes UTF-8', async () => {
    const f = fileFrom(new TextEncoder().encode('采购订单 · quarterly review'), 'a.txt')
    const d = await decodeTextFile(f)
    expect(d.encoding).toBe('utf-8')
    expect(d.text).toContain('采购订单')
    expect(d.lossy).toBe(false)
    expect(d.truncated).toBe(false)
  })

  it('falls back to GB18030 when UTF-8 produces replacement characters', async () => {
    // "金额：128,400 元" encoded as GB18030.
    const gb = Uint8Array.from([
      0xbd, 0xf0, 0xb6, 0xee, 0xa3, 0xba, 0x31, 0x32, 0x38, 0x2c, 0x34, 0x30, 0x30, 0x20, 0xd4, 0xaa,
    ])
    const d = await decodeTextFile(fileFrom(gb, 'gb.csv'))
    expect(d.encoding).toBe('gb18030')
    expect(d.text).toContain('金额')
  })

  it('truncates large files instead of reading everything', async () => {
    const big = new Uint8Array(2048 * 1024).fill(0x61)
    const d = await decodeTextFile(fileFrom(big, 'big.log'), 1024 * 1024)
    expect(d.truncated).toBe(true)
    expect(d.text.length).toBeLessThanOrEqual(1024 * 1024)
  })
})

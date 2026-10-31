export interface DecodedText {
  text: string
  truncated: boolean
  encoding: 'utf-8' | 'gb18030'
  /** True when the file did not decode as UTF-8 and no better encoding was found. */
  lossy: boolean
}

const REPLACEMENT = '\uFFFD'

function badRatio(text: string): number {
  if (!text) return 0
  let bad = 0
  for (const ch of text) if (ch === REPLACEMENT) bad += 1
  return bad / text.length
}

/**
 * Decodes a text-ish file for preview. Reads at most `maxBytes` so a 100 MB log cannot lock
 * the main thread, and falls back to GB18030 when UTF-8 produces replacement characters
 * (the audit corpus contains GB18030 CSVs).
 */
export async function decodeTextFile(file: File, maxBytes = 512 * 1024): Promise<DecodedText> {
  const slice = file.slice(0, maxBytes)
  const bytes = new Uint8Array(await slice.arrayBuffer())
  let text = new TextDecoder('utf-8').decode(bytes)
  let encoding: 'utf-8' | 'gb18030' = 'utf-8'
  const utf8Bad = badRatio(text)
  if (utf8Bad > 0.002) {
    try {
      const alt = new TextDecoder('gb18030').decode(bytes)
      if (badRatio(alt) < utf8Bad) {
        text = alt
        encoding = 'gb18030'
      }
    } catch {
      /* browser without gb18030 support: keep utf-8 */
    }
  }
  return {
    text,
    truncated: file.size > maxBytes,
    encoding,
    lossy: badRatio(text) > 0.002,
  }
}

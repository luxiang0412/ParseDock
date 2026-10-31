import { useEffect, useState } from 'react'
import { decodeTextFile, type DecodedText } from './text'

export function TextPreview({ file, mono = true }: { file: File; mono?: boolean }) {
  const [decoded, setDecoded] = useState<DecodedText | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    decodeTextFile(file)
      .then((d) => !cancelled && setDecoded(d))
      .catch((e: unknown) => !cancelled && setError(String(e)))
    return () => {
      cancelled = true
    }
  }, [file])

  if (error) return <p className="p-4 text-sm text-danger-500">Could not read the file: {error}</p>
  if (!decoded) return <p className="p-4 text-sm text-ink-500">Reading file…</p>

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-3 border-b border-ink-200 px-3 py-2 text-xs text-ink-500 dark:border-ink-800">
        <span className="font-mono">{decoded.encoding.toUpperCase()}</span>
        {decoded.lossy && <span className="text-warn-500">some characters could not be decoded</span>}
        {decoded.truncated && <span>preview truncated to the first 512 KiB</span>}
      </div>
      <pre
        className={`min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-words p-4 text-[0.85rem] leading-6 ${mono ? 'font-mono' : ''}`}
      >
        {decoded.text}
      </pre>
    </div>
  )
}

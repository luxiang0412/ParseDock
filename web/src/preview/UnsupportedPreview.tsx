import { humanBytes } from '../features/playground/format'

export function UnsupportedPreview({ file, reason }: { file: File; reason: string }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-8 text-center">
      <div className="rounded-full border border-dashed border-ink-300 p-3 text-ink-400 dark:border-ink-700">
        <svg width="22" height="22" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path d="M7 3h7l5 5v13H7z" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" />
          <path d="M14 3v5h5" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" />
        </svg>
      </div>
      <div>
        <p className="text-sm font-medium">{file.name}</p>
        <p className="mt-1 font-mono text-xs text-ink-500">{humanBytes(file.size)}</p>
      </div>
      <p className="max-w-sm text-sm text-ink-500">{reason}</p>
    </div>
  )
}

import clsx from 'clsx'
import { useCallback, useId, useRef, useState } from 'react'
import { ACCEPTED_EXTENSIONS, humanBytes, isAccepted } from '../features/playground/format'

export function Dropzone({
  onSelect,
  disabled,
}: {
  onSelect: (file: File) => void
  disabled?: boolean
}) {
  const inputId = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const [dragging, setDragging] = useState(false)
  const [rejected, setRejected] = useState<string | null>(null)

  const accept = useCallback(
    (files: FileList | null) => {
      const file = files?.[0]
      if (!file) return
      if (!isAccepted(file)) {
        setRejected(
          `${file.name} is not an accepted type. Accepted: ${ACCEPTED_EXTENSIONS.join(' ')}`,
        )
        return
      }
      setRejected(null)
      onSelect(file)
    },
    [onSelect],
  )

  return (
    <div className="w-full">
      <label
        htmlFor={inputId}
        onDragOver={(e) => {
          e.preventDefault()
          if (!disabled) setDragging(true)
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault()
          setDragging(false)
          if (!disabled) accept(e.dataTransfer.files)
        }}
        className={clsx(
          'group flex cursor-pointer flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed px-6 py-10 text-center transition-colors',
          dragging
            ? 'border-accent-500 bg-accent-50 dark:bg-accent-600/10'
            : 'border-ink-300 bg-white hover:border-accent-500/60 hover:bg-ink-50 dark:border-ink-700 dark:bg-ink-900/40 dark:hover:bg-ink-900',
          disabled && 'pointer-events-none opacity-60',
        )}
      >
        <span
          aria-hidden="true"
          className="grid h-11 w-11 place-items-center rounded-full border border-ink-200 bg-ink-50 text-ink-500 group-hover:text-accent-600 dark:border-ink-700 dark:bg-ink-900"
        >
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none">
            <path d="M12 16V4m0 0L7.5 8.5M12 4l4.5 4.5" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
            <path d="M4 15v3.5A1.5 1.5 0 0 0 5.5 20h13a1.5 1.5 0 0 0 1.5-1.5V15" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
          </svg>
        </span>
        <span className="text-sm font-medium">
          Drop a document here, or <span className="text-accent-700 underline underline-offset-2 dark:text-accent-300">browse</span>
        </span>
        <span className="max-w-md text-xs leading-5 text-ink-500">
          PDF, Word, Excel, PowerPoint, CSV/TSV, text/Markdown, images and HTML. The file is
          uploaded to the ParseDock server for parsing; the preview on the left stays local.
        </span>
        <input
          ref={inputRef}
          id={inputId}
          type="file"
          className="sr-only"
          accept={ACCEPTED_EXTENSIONS.join(',')}
          onChange={(e) => accept(e.target.files)}
        />
      </label>
      {rejected && (
        <p role="alert" className="mt-2 text-xs text-danger-500">
          {rejected}
        </p>
      )}
      <p className="mt-2 text-center text-[0.7rem] text-ink-400">
        No file is retained by the browser beyond this tab · server results follow the API TTL
        until you release them
      </p>
      <span className="sr-only">{humanBytes(0)}</span>
    </div>
  )
}

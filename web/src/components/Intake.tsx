import { useRef } from 'react'
import type { Mode } from '../api/client'
import { Dropzone } from './Dropzone'
import { Button } from './Button'
import { ModeToggle } from './ModeToggle'
import { ACCEPTED_EXTENSIONS, humanBytes, previewKind } from '../features/playground/format'

const MODE_HINT: Record<Mode, string> = {
  auto: 'fast engine, escalating scans when OCR quality is low',
  fast: 'fastest engine; best for text-layer PDFs and Office files',
  accurate: 'prefers structure and provenance; slower, and results still vary by format',
}

/** The file picker + mode + start button, shown before and after a job exists. */
export function Intake({
  file,
  mode,
  busy,
  onSelect,
  onMode,
  onStart,
  onReset,
  hasJob,
}: {
  file: File | null
  mode: Mode
  busy: boolean
  onSelect: (f: File) => void
  onMode: (m: Mode) => void
  onStart: () => void
  onReset: () => void
  hasJob: boolean
}) {
  const pickerRef = useRef<HTMLInputElement>(null)
  if (!file) {
    return (
      <div className="mx-auto w-full max-w-3xl px-4 py-8">
        <Dropzone onSelect={onSelect} />
        <div className="mt-5 flex flex-wrap items-center justify-center gap-3">
          <span className="text-xs text-ink-500">Parse mode</span>
          <ModeToggle value={mode} onChange={onMode} />
          <p className="w-full text-center text-[0.7rem] text-ink-400">{MODE_HINT[mode]}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="border-b border-ink-200 bg-white px-4 py-3 dark:border-ink-800 dark:bg-ink-950">
      <div className="mx-auto flex max-w-[1600px] flex-wrap items-center gap-x-4 gap-y-3">
        <div className="flex w-full min-w-0 items-center gap-3 sm:w-auto sm:flex-1">
          <span
            aria-hidden="true"
            className="grid h-9 w-9 shrink-0 place-items-center rounded-md border border-ink-200 bg-ink-50 font-mono text-[0.6rem] font-semibold uppercase text-ink-600 dark:border-ink-700 dark:bg-ink-900 dark:text-ink-300"
          >
            {previewKind(file.name).slice(0, 4)}
          </span>
          <div className="min-w-0">
            <p className="truncate text-sm font-medium">{file.name}</p>
            <p className="font-mono text-[0.7rem] text-ink-500">
              {humanBytes(file.size)} · {file.type || 'unknown type'}
            </p>
          </div>
        </div>

        <div className="flex flex-1 items-center gap-2 sm:flex-none">
          <span className="hidden text-xs text-ink-500 sm:inline">Mode</span>
          <ModeToggle value={mode} onChange={onMode} disabled={busy} />
        </div>

        <div className="flex items-center gap-2">
          <input
            ref={pickerRef}
            type="file"
            className="sr-only"
            accept={ACCEPTED_EXTENSIONS.join(',')}
            onChange={(e) => {
              const picked = e.target.files?.[0]
              e.target.value = ''
              if (picked) onSelect(picked)
            }}
            aria-label="Choose another document"
          />
          <Button
            variant="ghost"
            size="sm"
            onClick={() => pickerRef.current?.click()}
            disabled={busy}
          >
            {hasJob ? 'New file' : 'Change'}
          </Button>
          {!hasJob && (
            <>
              <Button variant="ghost" size="sm" onClick={onReset} disabled={busy}>
                Clear
              </Button>
              <Button size="sm" onClick={onStart} disabled={busy}>
                {busy ? 'Parsing…' : 'Parse'}
              </Button>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

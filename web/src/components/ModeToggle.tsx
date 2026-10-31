import clsx from 'clsx'
import type { Mode } from '../api/client'

const MODES: { id: Mode; label: string; hint: string }[] = [
  { id: 'auto', label: 'Auto', hint: 'Fast engine, escalating scans when OCR quality is low.' },
  { id: 'fast', label: 'Fast', hint: 'Fastest engine. Best for text-layer PDFs and Office files.' },
  {
    id: 'accurate',
    label: 'Accurate',
    hint: 'Structure/source-preferring engine. Slower; per-format results still vary.',
  },
]

export function ModeToggle({
  value,
  onChange,
  disabled,
}: {
  value: Mode
  onChange: (m: Mode) => void
  disabled?: boolean
}) {
  return (
    <div
      role="radiogroup"
      aria-label="Parse mode"
      className="inline-flex rounded-lg border border-ink-200 bg-ink-100/60 p-0.5 dark:border-ink-800 dark:bg-ink-900"
    >
      {MODES.map((m) => (
        <button
          key={m.id}
          type="button"
          role="radio"
          aria-checked={value === m.id}
          title={m.hint}
          disabled={disabled}
          onClick={() => onChange(m.id)}
          className={clsx(
            'rounded-[6px] px-3 py-1.5 text-xs font-medium transition-colors disabled:opacity-60',
            value === m.id
              ? 'bg-white text-ink-900 shadow-sm dark:bg-ink-800 dark:text-white'
              : 'text-ink-500 hover:text-ink-800 dark:hover:text-ink-200',
          )}
        >
          {m.label}
        </button>
      ))}
    </div>
  )
}

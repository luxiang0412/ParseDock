import clsx from 'clsx'
import type { Mode } from '../api/client'
import { useI18n } from '../i18n'
import { MODE_HINT, MODE_LABEL, MODES } from '../features/playground/modes'

export function ModeToggle({
  value,
  onChange,
  disabled,
}: {
  value: Mode
  onChange: (m: Mode) => void
  disabled?: boolean
}) {
  const { t } = useI18n()
  return (
    <div
      role="radiogroup"
      aria-label={t('mode.group')}
      className="inline-flex rounded-lg border border-ink-200 bg-ink-100/60 p-0.5 dark:border-ink-800 dark:bg-ink-900"
    >
      {MODES.map((id) => (
        <button
          key={id}
          type="button"
          role="radio"
          aria-checked={value === id}
          title={t(MODE_HINT[id])}
          disabled={disabled}
          onClick={() => onChange(id)}
          className={clsx(
            'rounded-[6px] px-3 py-1.5 text-xs font-medium transition-colors disabled:opacity-60',
            value === id
              ? 'bg-white text-ink-900 shadow-sm dark:bg-ink-800 dark:text-white'
              : 'text-ink-500 hover:text-ink-800 dark:hover:text-ink-200',
          )}
        >
          {t(MODE_LABEL[id])}
        </button>
      ))}
    </div>
  )
}

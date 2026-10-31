import { friendlyForCode } from '../api/errors'
import { useI18n } from '../i18n'

export function ErrorNote({
  code,
  message,
  onDismiss,
}: {
  code?: string
  message: string
  onDismiss?: () => void
}) {
  const { t } = useI18n()
  return (
    <div
      role="alert"
      className="flex items-start gap-3 rounded-lg border border-danger-500/30 bg-danger-500/5 px-3 py-2.5 text-sm"
    >
      <span aria-hidden="true" className="mt-0.5 text-danger-500">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
          <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="1.8" />
          <path d="M12 7.5v5.5M12 16.2v.1" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
        </svg>
      </span>
      <div className="min-w-0 flex-1">
        <p className="font-medium">{friendlyForCode(code, t, message)}</p>
        <p className="mt-0.5 font-mono text-[0.7rem] text-ink-500">
          {code ? `${code} · ${message}` : message}
        </p>
      </div>
      {onDismiss && (
        <button
          type="button"
          onClick={onDismiss}
          className="rounded p-1 text-ink-500 hover:bg-ink-100 dark:hover:bg-ink-900"
          aria-label={t('app.dismiss')}
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <path d="M6 6l12 12M18 6L6 18" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
          </svg>
        </button>
      )}
    </div>
  )
}

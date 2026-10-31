import { useEffect, useRef } from 'react'
import { useI18n } from '../i18n'
import { Button } from './Button'

export function HelpDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n()
  const ref = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (open && !el.open) el.showModal()
    if (!open && el.open) el.close()
  }, [open])

  return (
    <dialog
      ref={ref}
      onClose={onClose}
      onClick={(e) => {
        if (e.target === ref.current) onClose()
      }}
      aria-labelledby="help-title"
      className="w-[min(36rem,92vw)] rounded-xl border border-ink-200 bg-white p-0 text-ink-900 shadow-xl backdrop:bg-ink-950/40 dark:border-ink-800 dark:bg-ink-900 dark:text-ink-100"
    >
      <div className="flex items-start justify-between gap-4 border-b border-ink-200 px-5 py-4 dark:border-ink-800">
        <div>
          <h2 id="help-title" className="text-sm font-semibold">
            {t('help.title')}
          </h2>
          <p className="mt-0.5 text-xs text-ink-500">{t('help.subtitle')}</p>
        </div>
        <Button size="sm" variant="ghost" onClick={onClose}>
          {t('help.close')}
        </Button>
      </div>
      <ol className="space-y-3 px-5 py-4 text-sm">
        <li>
          <span className="font-medium">{t('help.step1.bold')}</span>
          {t('help.step1')}
        </li>
        <li>
          <span className="font-medium">{t('help.step2.bold')}</span>
          {t('help.step2a')}
          <em>{t('help.step2.fast')}</em>
          {t('help.step2.mid')}
          <em>{t('help.step2.accurate')}</em>
          {t('help.step2.end')}
        </li>
        <li>
          <span className="font-medium">{t('help.step3.bold')}</span>
          {t('help.step3')}
        </li>
        <li>
          <span className="font-medium">{t('help.step4.bold')}</span>
          {t('help.step4')}
        </li>
      </ol>
      <p className="border-t border-ink-200 px-5 py-3 text-xs text-ink-500 dark:border-ink-800">
        {t('help.footer')}
      </p>
    </dialog>
  )
}

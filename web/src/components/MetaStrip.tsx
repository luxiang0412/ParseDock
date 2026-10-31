import type { Job } from '../api/client'
import type { PlaygroundState } from '../features/playground/phase'
import { describeExpiry, humanMs } from '../features/playground/format'
import { useI18n } from '../i18n'

function Chip({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <dt className="text-[0.7rem] uppercase tracking-wide text-ink-400">{label}</dt>
      <dd className={`text-xs ${mono ? 'font-mono' : ''}`}>{value}</dd>
    </div>
  )
}

export function MetaStrip({
  job,
  ir,
  markdown,
}: {
  job: Job | null
  ir: PlaygroundState['ir']
  markdown: string | null
}) {
  const { t } = useI18n()
  if (!job || job.status !== 'completed') return null
  const chars = markdown ? [...markdown].length : undefined
  return (
    <dl className="flex flex-wrap items-center gap-x-5 gap-y-1 border-b border-ink-200 bg-ink-50/60 px-4 py-2 dark:border-ink-800 dark:bg-ink-900/40">
      <Chip label={t('meta.engine')} value={job.engine || '–'} />
      <Chip label={t('meta.device')} value={ir?.device ?? job.requested_device ?? 'auto'} />
      <Chip label={t('meta.pages')} value={job.page_count ? String(job.page_count) : '–'} />
      <Chip
        label={t('meta.chars')}
        value={chars !== undefined ? chars.toLocaleString() : '–'}
        mono
      />
      <Chip label={t('meta.tables')} value={ir?.tables !== undefined ? String(ir.tables) : '–'} mono />
      <Chip label={t('meta.parse')} value={humanMs(ir?.parseMs, t)} mono />
      {job.warning_count ? (
        <Chip
          label={t('meta.warnings')}
          value={
            ir?.warningCodes?.length ? ir.warningCodes.join(', ') : String(job.warning_count)
          }
        />
      ) : null}
      <Chip label={t('meta.expires')} value={describeExpiry(job.result_expires_at, t)} mono />
      {job.result_version && (
        <Chip
          label={t('meta.version')}
          value={job.result_version.replace('sha256:', '').slice(0, 10)}
          mono
        />
      )}
    </dl>
  )
}

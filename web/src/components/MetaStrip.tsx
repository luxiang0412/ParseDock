import type { Job } from '../api/client'
import type { PlaygroundState } from '../features/playground/phase'
import { describeExpiry, humanMs } from '../features/playground/format'

function Chip({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <dt className="text-[0.7rem] uppercase tracking-wide text-ink-400">{label}</dt>
      <dd className={`text-xs ${mono ? 'font-mono' : ''}`}>{value}</dd>
    </div>
  )
}

export function MetaStrip({ job, ir, markdown }: { job: Job | null; ir: PlaygroundState['ir']; markdown: string | null }) {
  if (!job || job.status !== 'completed') return null
  const chars = markdown ? [...markdown].length : undefined
  return (
    <dl className="flex flex-wrap items-center gap-x-5 gap-y-1 border-b border-ink-200 bg-ink-50/60 px-4 py-2 dark:border-ink-800 dark:bg-ink-900/40">
      <Chip label="engine" value={job.engine || '–'} />
      <Chip label="device" value={ir?.device ?? job.requested_device ?? 'auto'} />
      <Chip label="pages" value={job.page_count ? String(job.page_count) : '–'} />
      <Chip label="chars" value={chars !== undefined ? chars.toLocaleString() : '–'} mono />
      <Chip label="tables" value={ir?.tables !== undefined ? String(ir.tables) : '–'} mono />
      <Chip label="parse" value={humanMs(ir?.parseMs)} mono />
      {job.warning_count ? (
        <Chip
          label="warnings"
          value={ir?.warningCodes?.length ? ir.warningCodes.join(', ') : String(job.warning_count)}
        />
      ) : null}
      <Chip
        label="expires"
        value={describeExpiry(job.result_expires_at)}
        mono
      />
      {job.result_version && (
        <Chip label="version" value={job.result_version.replace('sha256:', '').slice(0, 10)} mono />
      )}
    </dl>
  )
}

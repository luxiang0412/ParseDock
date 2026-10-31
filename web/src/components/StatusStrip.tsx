import type { Job } from '../api/client'
import type { Phase } from '../features/playground/phase'
import { describeExpiry, formatClock } from '../features/playground/format'
import { Button } from './Button'
import { Spinner } from './Spinner'

const LABEL: Record<Phase, string> = {
  idle: 'Ready',
  selected: 'Ready to parse',
  uploading: 'Uploading',
  queued: 'Queued',
  running: 'Parsing',
  completed: 'Completed',
  failed: 'Failed',
  cancelled: 'Cancelled',
  releasing: 'Releasing server copy',
  released: 'Server copy released',
  expired: 'Result expired',
  error: 'Error',
}

const TONE: Record<Phase, string> = {
  idle: 'text-ink-500',
  selected: 'text-ink-500',
  uploading: 'text-accent-700 dark:text-accent-300',
  queued: 'text-accent-700 dark:text-accent-300',
  running: 'text-accent-700 dark:text-accent-300',
  completed: 'text-ok-500',
  failed: 'text-danger-500',
  cancelled: 'text-ink-500',
  releasing: 'text-accent-700 dark:text-accent-300',
  released: 'text-ink-500',
  expired: 'text-warn-500',
  error: 'text-danger-500',
}

export function StatusStrip({
  phase,
  job,
  elapsedMs,
  canCancel,
  canRelease,
  onCancel,
  onRelease,
  releasing,
}: {
  phase: Phase
  job: Job | null
  elapsedMs: number | null
  canCancel: boolean
  canRelease: boolean
  onCancel: () => void
  onRelease: () => void
  releasing: boolean
}) {
  const working = phase === 'uploading' || phase === 'queued' || phase === 'running'
  const percent = job?.progress_percent ?? 0
  const stage = job?.progress_stage ?? (phase === 'uploading' ? 'sending file' : '')

  return (
    <div
      data-testid="job-status"
      className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-ink-200 bg-white px-4 py-2 text-sm dark:border-ink-800 dark:bg-ink-950"
    >
      <div className="flex items-center gap-2" role="status" aria-live="polite">
        {working && <Spinner size={13} />}
        <span className={`font-medium ${TONE[phase]}`}>{LABEL[phase]}</span>
        {stage && working && <span className="font-mono text-[0.7rem] text-ink-500">{stage}</span>}
      </div>

      {working && (
        <div className="flex min-w-[10rem] flex-1 items-center gap-2">
          <div
            className="h-1.5 flex-1 overflow-hidden rounded-full bg-ink-200 dark:bg-ink-800"
            role="progressbar"
            aria-label="Parse progress"
            aria-valuemin={0}
            aria-valuemax={100}
            {...(percent > 0 ? { 'aria-valuenow': percent } : {})}
          >
            <div
              className={`h-full rounded-full bg-accent-600 transition-[width] duration-500 ${
                percent > 0 ? '' : 'w-1/3 animate-pulse'
              }`}
              style={percent > 0 ? { width: `${Math.min(100, percent)}%` } : undefined}
            />
          </div>
          {percent > 0 && <span className="font-mono text-[0.7rem] text-ink-500">{percent}%</span>}
        </div>
      )}

      {elapsedMs !== null && (
        <span className="font-mono text-[0.7rem] text-ink-500" title="Time since the job was created">
          {formatClock(elapsedMs)}
        </span>
      )}

      {job?.result_expires_at && phase === 'completed' && (
        <span className="text-[0.7rem] text-ink-500">
          server copy expires {describeExpiry(job.result_expires_at)}
        </span>
      )}
      {job?.retention_status === 'release_pending' && (
        <span className="text-[0.7rem] text-ink-500">deletion in progress on the server…</span>
      )}
      {phase === 'released' && (
        <span className="text-[0.7rem] text-ink-500">
          downloaded Markdown stays readable here; new downloads return 410
        </span>
      )}

      <div className="ml-auto flex items-center gap-2">
        {canCancel && (
          <Button size="sm" variant="danger" onClick={onCancel}>
            Cancel job
          </Button>
        )}
        {canRelease && (
          <Button size="sm" variant="secondary" onClick={onRelease} disabled={releasing}>
            {releasing ? 'Releasing…' : 'Release server copy'}
          </Button>
        )}
      </div>
    </div>
  )
}

import type { Job, Mode } from '../../api/client'

export type Phase =
  | 'idle'
  | 'selected'
  | 'uploading'
  | 'queued'
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'releasing'
  | 'released'
  | 'expired'
  | 'error'

export interface PlaygroundState {
  phase: Phase
  file: File | null
  mode: Mode
  job: Job | null
  markdown: string | null
  resultVersion: string | null
  /** Optional IR metadata (engine metrics, warning codes); null when too large or unavailable. */
  ir: { parseMs?: number; warningCodes?: string[]; tables?: number; device?: string } | null
  error: { code?: string; message: string } | null
  /** Epoch ms when the job reached a terminal state, for the elapsed timer. */
  finishedAt: number | null
}

export const initialState: PlaygroundState = {
  phase: 'idle',
  file: null,
  mode: 'auto',
  job: null,
  markdown: null,
  resultVersion: null,
  ir: null,
  error: null,
  finishedAt: null,
}

export type Action =
  | { type: 'select'; file: File }
  | { type: 'mode'; mode: Mode }
  | { type: 'start' }
  | { type: 'uploaded'; job: Job }
  | { type: 'job'; job: Job }
  | { type: 'result'; markdown: string; resultVersion: string | null }
  | { type: 'ir'; ir: PlaygroundState['ir'] }
  | { type: 'progress'; percent?: number; stage?: string }
  | { type: 'releasing' }
  | { type: 'error'; code?: string; message: string }
  | { type: 'reset' }

/**
 * The backend's status and retention status are the only source of truth; `phase` is a
 * projection of them plus the two local actions the user can take (upload, release).
 */
export function phaseOf(job: Job | null): Phase {
  if (!job) return 'idle'
  switch (job.retention_status) {
    case 'released':
      return 'released'
    case 'expired':
      return 'expired'
    default:
      break
  }
  switch (job.status) {
    case 'queued':
      return 'queued'
    case 'running':
      return 'running'
    case 'completed':
      return 'completed'
    case 'failed':
      return 'failed'
    case 'cancelled':
      return 'cancelled'
    default:
      return 'error'
  }
}

export function reducer(state: PlaygroundState, action: Action): PlaygroundState {
  switch (action.type) {
    case 'select':
      return { ...initialState, phase: 'selected', file: action.file, mode: state.mode }
    case 'mode':
      return { ...state, mode: action.mode }
    case 'start':
      return { ...state, phase: 'uploading', job: null, markdown: null, resultVersion: null, ir: null, error: null, finishedAt: null }
    case 'uploaded':
      return { ...state, job: action.job, phase: phaseOf(action.job) }
    case 'job': {
      const next = phaseOf(action.job)
      const terminal = next !== 'queued' && next !== 'running'
      const keepRelease = state.phase === 'releasing' && (next === 'completed' || next === 'released')
      return {
        ...state,
        job: action.job,
        phase: keepRelease && next === 'completed' ? 'releasing' : next,
        finishedAt: terminal ? (state.finishedAt ?? Date.now()) : null,
      }
    }
    case 'result':
      return { ...state, markdown: action.markdown, resultVersion: action.resultVersion ?? state.resultVersion }
    case 'ir':
      return { ...state, ir: action.ir }
    case 'progress': {
      if (!state.job) return state
      return {
        ...state,
        job: {
          ...state.job,
          ...(action.percent !== undefined ? { progress_percent: action.percent } : {}),
          ...(action.stage ? { progress_stage: action.stage } : {}),
        },
      }
    }
    case 'releasing':
      return { ...state, phase: 'releasing', error: null }
    case 'error':
      return { ...state, phase: 'error', error: { code: action.code, message: action.message } }
    case 'reset':
      return { ...initialState, mode: state.mode }
    default:
      return state
  }
}

/** A job still counts as "server-side data not yet released". */
export function holdsServerCopy(job: Job | null): boolean {
  if (!job) return false
  if (job.retention_status !== 'active') return false
  return job.status === 'queued' || job.status === 'running' || job.status === 'completed' || job.status === 'failed'
}

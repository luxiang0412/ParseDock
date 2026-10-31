import { describe, expect, it } from 'vitest'
import type { Job } from '../../api/client'
import { holdsServerCopy, initialState, phaseOf, reducer } from './phase'

function job(over: Partial<Job> = {}): Job {
  return {
    job_id: 'job_1',
    status: 'queued',
    retention_status: 'active',
    created_at: '2026-01-01T00:00:00Z',
    status_url: '/v1/jobs/job_1',
    ...over,
  }
}

describe('phaseOf', () => {
  it('maps backend status and retention to a phase', () => {
    expect(phaseOf(null)).toBe('idle')
    expect(phaseOf(job({ status: 'queued' }))).toBe('queued')
    expect(phaseOf(job({ status: 'running' }))).toBe('running')
    expect(phaseOf(job({ status: 'completed' }))).toBe('completed')
    expect(phaseOf(job({ status: 'failed' }))).toBe('failed')
    expect(phaseOf(job({ status: 'cancelled' }))).toBe('cancelled')
  })

  it('retention wins over processing status', () => {
    expect(phaseOf(job({ status: 'completed', retention_status: 'released' }))).toBe('released')
    expect(phaseOf(job({ status: 'completed', retention_status: 'expired' }))).toBe('expired')
  })
})

describe('reducer', () => {
  const file = new File(['hello'], 'a.txt', { type: 'text/plain' })

  it('selecting a file clears any previous job', () => {
    const withJob = reducer(
      { ...initialState, job: job({ status: 'completed' }), markdown: '# hi', phase: 'completed' },
      { type: 'select', file },
    )
    expect(withJob.phase).toBe('selected')
    expect(withJob.file).toBe(file)
    expect(withJob.job).toBeNull()
    expect(withJob.markdown).toBeNull()
  })

  it('keeps the chosen mode across a reset', () => {
    const s = reducer({ ...initialState, mode: 'accurate' }, { type: 'reset' })
    expect(s.mode).toBe('accurate')
    expect(s.phase).toBe('idle')
  })

  it('records the finish time once', () => {
    const running = reducer(
      { ...initialState, file },
      { type: 'uploaded', job: job({ status: 'running' }) },
    )
    const done = reducer(running, { type: 'job', job: job({ status: 'completed' }) })
    expect(done.phase).toBe('completed')
    expect(done.finishedAt).not.toBeNull()
    const again = reducer(done, { type: 'job', job: job({ status: 'completed' }) })
    expect(again.finishedAt).toBe(done.finishedAt)
  })

  it('progress updates patch the job without touching the phase', () => {
    const base = reducer({ ...initialState, file }, { type: 'job', job: job({ status: 'running' }) })
    const next = reducer(base, { type: 'progress', percent: 40, stage: 'parsing' })
    expect(next.phase).toBe('running')
    expect(next.job?.progress_percent).toBe(40)
    expect(next.job?.progress_stage).toBe('parsing')
  })

  it('an error phase does not silently look completed', () => {
    const s = reducer({ ...initialState, file }, { type: 'error', code: 'rate_limited', message: 'slow down' })
    expect(s.phase).toBe('error')
    expect(s.error?.code).toBe('rate_limited')
  })
})

describe('holdsServerCopy', () => {
  it('is true only while the server still has the input/result', () => {
    expect(holdsServerCopy(null)).toBe(false)
    expect(holdsServerCopy(job({ status: 'running' }))).toBe(true)
    expect(holdsServerCopy(job({ status: 'completed' }))).toBe(true)
    expect(holdsServerCopy(job({ status: 'completed', retention_status: 'release_pending' }))).toBe(false)
    expect(holdsServerCopy(job({ status: 'completed', retention_status: 'released' }))).toBe(false)
  })
})

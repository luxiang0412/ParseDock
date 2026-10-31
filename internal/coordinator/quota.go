package coordinator

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
)

// ownerLocks serialises quota admission per owner. A single coordinator owns the
// database, so an in-process lock is sufficient; without it, concurrent uploads
// can all observe "below the limit" before any of them inserts a job row.
type ownerLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (l *ownerLocks) get(owner string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]*sync.Mutex{}
	}
	mu, ok := l.m[owner]
	if !ok {
		mu = &sync.Mutex{}
		l.m[owner] = mu
	}
	return mu
}

// lockOwner blocks until this owner's admission slot is free and returns the
// unlock function. Callers must hold it from the quota check to the job insert.
func (s *Service) lockOwner(owner string) func() {
	mu := s.admit.get(owner)
	mu.Lock()
	return mu.Unlock
}

// Quota bounds one owner's resource usage. Zero means "not configured", i.e.
// unlimited. Quotas are enforced by the Coordinator (the only stateful
// component) so they hold across any number of stateless API replicas.
type Quota struct {
	MaxConcurrent   int   // queued + running jobs
	MaxJobsPerDay   int   // rolling 24 h window
	MaxBytesPerDay  int64 // rolling 24 h window of input bytes
	MaxStorageBytes int64 // bytes of live result artifacts for this owner
}

// Enabled reports whether any limit is configured.
func (q Quota) Enabled() bool {
	return q.MaxConcurrent > 0 || q.MaxJobsPerDay > 0 || q.MaxBytesPerDay > 0 || q.MaxStorageBytes > 0
}

// usage is the current consumption of one owner.
type usage struct {
	concurrent int
	jobsToday  int
	bytesToday int64
}

// ownerUsage returns the owner's live concurrency (queued/running jobs, regardless
// of age) plus the durable rolling-window usage from owner_usage. The two are
// deliberately separate: an old queued job must not consume today's allowance, and
// purging job metadata must not erase consumption.
func (s *Service) ownerUsage(ctx context.Context, owner string, since time.Time) (usage, error) {
	var u usage
	if err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM jobs WHERE owner_id = ? AND processing_status IN ('queued','running')`,
		owner).Scan(&u.concurrent); err != nil {
		return u, err
	}
	if err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(bytes), 0) FROM owner_usage
		WHERE owner_id = ? AND created_at >= ?`, owner, ts(since)).
		Scan(&u.jobsToday, &u.bytesToday); err != nil {
		return u, err
	}
	return u, nil
}

// recordUsage persists consumption at admission. It runs in the same transaction
// as the job insert (see CreateJobStaged) so a crash cannot lose the accounting.
func recordUsage(ctx context.Context, tx *sql.Tx, jobID, owner string, bytes int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO owner_usage(job_id, owner_id, bytes, created_at)
		VALUES(?,?,?,?) ON CONFLICT(job_id) DO NOTHING`, jobID, owner, bytes, ts(nowUTC()))
	return err
}

// enforceQuota checks the limits for a new upload of size bytes. It returns a
// 429 with Retry-After set when a limit is hit; the caller must not persist the
// job in that case.
func (s *Service) enforceQuota(ctx context.Context, owner string, size int64) error {
	if !s.Cfg.Quota.Enabled() {
		return nil
	}
	u, err := s.ownerUsage(ctx, owner, nowUTC().Add(-24*time.Hour))
	if err != nil {
		return NewError(500, jobs.ErrInternal, "quota lookup: "+err.Error())
	}
	q := s.Cfg.Quota
	if q.MaxConcurrent > 0 && u.concurrent >= q.MaxConcurrent {
		return &Error{Status: 429, Code: jobs.ErrQuotaExceeded, RetryAfter: 5,
			Message: fmt.Sprintf("concurrent job limit reached (%d); retry after one completes", q.MaxConcurrent)}
	}
	if q.MaxJobsPerDay > 0 && u.jobsToday >= q.MaxJobsPerDay {
		return &Error{Status: 429, Code: jobs.ErrQuotaExceeded, RetryAfter: 60,
			Message: fmt.Sprintf("daily job limit reached (%d per 24h)", q.MaxJobsPerDay)}
	}
	if q.MaxStorageBytes > 0 {
		var stored int64
		_ = s.DB.QueryRowContext(ctx, `SELECT bytes FROM owner_storage WHERE owner_id = ?`, owner).Scan(&stored)
		if stored+size > q.MaxStorageBytes {
			return &Error{Status: 429, Code: jobs.ErrQuotaExceeded, RetryAfter: 60,
				Message: fmt.Sprintf("storage quota reached (%d MiB of live results)", q.MaxStorageBytes>>20)}
		}
	}
	if q.MaxBytesPerDay > 0 && u.bytesToday+size > q.MaxBytesPerDay {
		return &Error{Status: 429, Code: jobs.ErrQuotaExceeded, RetryAfter: 60,
			Message: fmt.Sprintf("daily byte limit reached (%d MiB per 24h)", q.MaxBytesPerDay>>20)}
	}
	return nil
}

// audit writes one structured audit line. It must never contain document text,
// tokens or result bodies: only identities, ids, sizes and decisions.
func (s *Service) audit(ctx context.Context, event string, job *jobs.Job, extra map[string]any) {
	if s.Audit == nil {
		return
	}
	args := []any{
		"audit", true,
		"event", event,
		"owner_id", job.OwnerID,
		"job_id", job.ID,
		"format", job.Format,
		"bytes", job.InputSize,
		"request_id", requestID(ctx),
	}
	// The client address as resolved by the gateway (absent for worker-driven and
	// cleanup events, which have no client).
	if ip := clientIP(ctx); ip != "" {
		args = append(args, "client_ip", ip)
	}
	for k, v := range extra {
		args = append(args, k, v)
	}
	s.Audit.Info("audit", args...)
}

// adjustStorage changes an owner's live-result accounting inside tx. Deltas are
// clamped at zero so a double release cannot make the counter negative.
func adjustStorage(ctx context.Context, tx *sql.Tx, owner string, delta int64) error {
	if owner == "" || delta == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO owner_storage(owner_id, bytes, updated_at) VALUES(?, MAX(?,0), ?)
		ON CONFLICT(owner_id) DO UPDATE SET bytes = MAX(owner_storage.bytes + ?, 0), updated_at = excluded.updated_at`,
		owner, delta, ts(nowUTC()), delta)
	return err
}

package coordinator

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
)

// Quota bounds one owner's resource usage. Zero means "not configured", i.e.
// unlimited. Quotas are enforced by the Coordinator (the only stateful
// component) so they hold across any number of stateless API replicas.
type Quota struct {
	MaxConcurrent  int   // queued + running jobs
	MaxJobsPerDay  int   // rolling 24 h window
	MaxBytesPerDay int64 // rolling 24 h window of input bytes
}

// Enabled reports whether any limit is configured.
func (q Quota) Enabled() bool {
	return q.MaxConcurrent > 0 || q.MaxJobsPerDay > 0 || q.MaxBytesPerDay > 0
}

// usage is the current consumption of one owner.
type usage struct {
	concurrent int
	jobsToday  int
	bytesToday int64
}

func (s *Service) ownerUsage(ctx context.Context, owner string, since time.Time) (usage, error) {
	var u usage
	err := s.DB.QueryRowContext(ctx, `
		SELECT
		  COUNT(*) FILTER (WHERE processing_status IN ('queued','running')),
		  COUNT(*),
		  COALESCE(SUM(input_size), 0)
		FROM jobs WHERE owner_id = ? AND (created_at >= ? OR processing_status IN ('queued','running'))`,
		owner, ts(since)).Scan(&u.concurrent, &u.jobsToday, &u.bytesToday)
	if err != nil && err != sql.ErrNoRows {
		return u, err
	}
	return u, nil
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
	for k, v := range extra {
		args = append(args, k, v)
	}
	s.Audit.Info("audit", args...)
}

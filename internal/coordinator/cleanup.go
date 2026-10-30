package coordinator

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// CleanupReport summarises one cleanup pass (logged and exposed by /readyz).
type CleanupReport struct {
	StartedAt        time.Time `json:"started_at"`
	DurationMs       int64     `json:"duration_ms"`
	ExpiredQueued    int       `json:"expired_queued"`
	LeasesReaped     int       `json:"worker_leases_reaped"`
	MarkedPending    int       `json:"marked_pending"`
	Released         int       `json:"released"`
	Expired          int       `json:"expired"`
	SkippedBusy      int       `json:"skipped_active_downloads"`
	TombstonesPurged int       `json:"tombstones_purged"`
	OrphanDirs       int       `json:"orphan_dirs_removed"`
	StaleParts       int       `json:"stale_parts_removed"`
	Errors           []string  `json:"errors,omitempty"`
}

// StartCleanup runs the cleaner until ctx is cancelled.
func (s *Service) StartCleanup(ctx context.Context) {
	s.RunCleanup(ctx)
	t := time.NewTicker(s.Cfg.CleanupInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RunCleanup(ctx)
		}
	}
}

// RunCleanup performs one idempotent pass. Safe to call concurrently with
// itself: every state change is a compare-and-set and file deletion is
// idempotent.
func (s *Service) RunCleanup(ctx context.Context) *CleanupReport {
	start := nowUTC()
	rep := &CleanupReport{StartedAt: start}
	s.cleanupRuns.Add(1)
	defer func() {
		rep.DurationMs = time.Since(start).Milliseconds()
		s.lastCleanup.Store(start)
		slog.Info("cleanup pass", "ms", rep.DurationMs, "released", rep.Released, "expired", rep.Expired,
			"pending", rep.MarkedPending, "busy", rep.SkippedBusy, "errors", len(rep.Errors))
	}()

	rep.LeasesReaped = s.reapWorkerLeases(ctx, rep)

	// 1. Result TTL reached -> expiry_pending. TTL counts from result commit.
	rep.MarkedPending += s.execCount(ctx, rep, `UPDATE jobs SET retention_status = 'expiry_pending', updated_at = ?
		WHERE processing_status = 'completed' AND retention_status = 'active'
		AND result_expires_at IS NOT NULL AND result_expires_at <= ?`, "expiry", ts(nowUTC()), ts(nowUTC()))

	// 2. Failed/cancelled artifacts are kept briefly for debugging.
	rep.MarkedPending += s.execCount(ctx, rep, `UPDATE jobs SET retention_status = 'release_pending', updated_at = ?
		WHERE processing_status IN ('failed','cancelled') AND retention_status = 'active'
		AND updated_at <= ?`, "failed_retention", ts(nowUTC()), ts(nowUTC().Add(-s.Cfg.FailedRetention)))

	// 3. Delete files for pending jobs that have no live download lease.
	rows, err := s.DB.QueryContext(ctx, `SELECT id, retention_status FROM jobs
		WHERE retention_status IN ('release_pending','expiry_pending')`)
	if err != nil {
		rep.Errors = append(rep.Errors, "query pending: "+err.Error())
	} else {
		type pending struct{ id, status string }
		var list []pending
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.id, &p.status); err == nil {
				list = append(list, p)
			}
		}
		_ = rows.Close()
		for _, p := range list {
			var busy int
			_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_leases WHERE job_id = ? AND expires_at > ?`,
				p.id, ts(nowUTC())).Scan(&busy)
			if busy > 0 {
				rep.SkippedBusy++
				continue
			}
			final := jobs.RetentionReleased
			if jobs.RetentionStatus(p.status) == jobs.RetentionExpiryPending {
				final = jobs.RetentionExpired
			}
			// Mark deleting first: a crash here is recovered because the
			// cleaner re-scans 'deleting' rows on the next pass. The lease check
			// is inside the UPDATE so a download that starts concurrently cannot
			// be deleted underneath (F03).
			res, err := s.DB.ExecContext(ctx, `UPDATE jobs SET retention_status = 'deleting', updated_at = ?
				WHERE id = ? AND retention_status IN ('release_pending','expiry_pending','deleting')
				  AND NOT EXISTS (SELECT 1 FROM download_leases
				                  WHERE job_id = ? AND expires_at > ?)`,
				ts(nowUTC()), p.id, p.id, ts(nowUTC()))
			if err != nil {
				rep.Errors = append(rep.Errors, "mark deleting: "+err.Error())
				continue
			}
			if n, _ := res.RowsAffected(); n == 0 {
				rep.SkippedBusy++
				continue
			}
			if err := s.Store.DeleteJob(p.id); err != nil {
				rep.Errors = append(rep.Errors, "delete files: "+err.Error())
				continue // stay in 'deleting' and retry next pass
			}
			tomb := ts(nowUTC().Add(s.Cfg.TombstoneTTL))
			if _, err := s.DB.ExecContext(ctx, `UPDATE jobs SET retention_status = ?, tombstone_expires_at = ?,
				result_version = COALESCE(result_version, ''), updated_at = ? WHERE id = ?`,
				string(final), tomb, ts(nowUTC()), p.id); err != nil {
				rep.Errors = append(rep.Errors, "finalize: "+err.Error())
				continue
			}
			// Release the owner's storage accounting before dropping the rows.
			var stored int64
			_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0) FROM artifacts WHERE job_id = ?`, p.id).Scan(&stored)
			if job, jerr := s.GetJob(ctx, p.id, ""); jerr == nil {
				if tx, terr := s.DB.BeginTx(ctx, nil); terr == nil {
					_ = adjustStorage(ctx, tx, job.OwnerID, -stored)
					_, _ = tx.ExecContext(ctx, `DELETE FROM artifacts WHERE job_id = ?`, p.id)
					_ = tx.Commit()
				}
			}
			_, _ = s.DB.ExecContext(ctx, `DELETE FROM download_leases WHERE job_id = ?`, p.id)
			s.appendEvent(ctx, p.id, string(final), map[string]any{"at": nowUTC()})
			if job, jerr := s.GetJob(ctx, p.id, ""); jerr == nil {
				s.audit(ctx, "job_"+string(final), job, map[string]any{"reason": p.status})
			}
			if final == jobs.RetentionReleased {
				rep.Released++
			} else {
				rep.Expired++
			}
		}
	}

	// 4. Purge tombstones after their grace window (metadata only).
	if res, err := s.DB.ExecContext(ctx, `DELETE FROM jobs WHERE retention_status IN ('released','expired')
		AND tombstone_expires_at IS NOT NULL AND tombstone_expires_at <= ?`, ts(nowUTC())); err == nil {
		n, _ := res.RowsAffected()
		rep.TombstonesPurged = int(n)
	} else {
		rep.Errors = append(rep.Errors, "purge tombstones: "+err.Error())
	}

	// 4b. Usage rows leave the 24 h window after 25 h; independent of job cleanup.
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM owner_usage WHERE created_at <= ?`,
		ts(nowUTC().Add(-25*time.Hour)))

	// 5. Expired download leases.
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM download_leases WHERE expires_at <= ?`, ts(nowUTC()))

	// 6. Jobs past max lifetime never run forever.
	if res, err := s.DB.ExecContext(ctx, `UPDATE jobs SET processing_status = 'failed', error_code = ?,
		error_message = 'maximum job lifetime exceeded', completed_at = ?, progress_stage = 'timeout',
		retention_status = 'release_pending', updated_at = ?
		WHERE processing_status IN ('queued','running') AND max_lifetime_at <= ?`,
		jobs.ErrTimeout, ts(nowUTC()), ts(nowUTC()), ts(nowUTC())); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			rep.ExpiredQueued = int(n)
		}
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE attempts SET state = 'expired' WHERE state = 'running'
		AND job_id IN (SELECT id FROM jobs WHERE processing_status IN ('failed','cancelled'))`)

	// 7. Stale *.part staging files.
	if n, err := s.Store.SweepParts(time.Hour); err == nil {
		rep.StaleParts = n
	} else {
		rep.Errors = append(rep.Errors, "sweep parts: "+err.Error())
	}

	// 8. Orphan result/input directories (crash between file commit and DB commit).
	rep.OrphanDirs = s.sweepOrphans(ctx, rep)
	return rep
}

func (s *Service) execCount(ctx context.Context, rep *CleanupReport, q, reason string, args ...any) int {
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		rep.Errors = append(rep.Errors, reason+": "+err.Error())
		return 0
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		slog.Info("retention pending", "reason", reason, "jobs", n)
	}
	return int(n)
}

// reapWorkerLeases requeues jobs whose worker stopped heartbeating.
func (s *Service) reapWorkerLeases(ctx context.Context, rep *CleanupReport) int {
	now := nowUTC()
	rows, err := s.DB.QueryContext(ctx, `SELECT job_id, attempt_no, worker_id FROM attempts
		WHERE state = 'running' AND lease_until <= ?`, ts(now))
	if err != nil {
		rep.Errors = append(rep.Errors, "reap query: "+err.Error())
		return 0
	}
	type lost struct {
		jobID   string
		attempt int
		worker  string
	}
	var list []lost
	for rows.Next() {
		var l lost
		if err := rows.Scan(&l.jobID, &l.attempt, &l.worker); err == nil {
			list = append(list, l)
		}
	}
	_ = rows.Close()

	n := 0
	for _, l := range list {
		var attemptsMade, maxAttempts int
		var status string
		if err := s.DB.QueryRowContext(ctx, `SELECT current_attempt, max_attempts, processing_status FROM jobs WHERE id = ?`,
			l.jobID).Scan(&attemptsMade, &maxAttempts, &status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			rep.Errors = append(rep.Errors, "reap job: "+err.Error())
			continue
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			rep.Errors = append(rep.Errors, "reap tx: "+err.Error())
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE attempts SET state = 'expired', error = 'lease expired'
			WHERE job_id = ? AND attempt_no = ? AND state = 'running'`, l.jobID, l.attempt); err != nil {
			_ = tx.Rollback()
			rep.Errors = append(rep.Errors, "reap attempt: "+err.Error())
			continue
		}
		if jobs.ProcessingStatus(status) == jobs.StatusRunning && attemptsMade < maxAttempts {
			backoff := time.Duration(1<<minInt(attemptsMade, 6)) * time.Second
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET processing_status = 'queued',
				progress = 0, progress_stage = 'requeued_after_lease_expiry', error_code = ?,
				error_message = 'worker lease expired', available_at = ?, updated_at = ?
				WHERE id = ? AND processing_status = 'running'`,
				jobs.ErrLeaseLost, ts(now.Add(backoff)), ts(now), l.jobID); err != nil {
				_ = tx.Rollback()
				rep.Errors = append(rep.Errors, "reap requeue: "+err.Error())
				continue
			}
		} else if jobs.ProcessingStatus(status) == jobs.StatusRunning {
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET processing_status = 'failed',
				error_code = ?, error_message = 'worker lease expired after max attempts',
				completed_at = ?, retention_status = 'release_pending', updated_at = ?
				WHERE id = ? AND processing_status = 'running'`,
				jobs.ErrLeaseLost, ts(now), ts(now), l.jobID); err != nil {
				_ = tx.Rollback()
				rep.Errors = append(rep.Errors, "reap fail: "+err.Error())
				continue
			}
		}
		if err := tx.Commit(); err != nil {
			rep.Errors = append(rep.Errors, "reap commit: "+err.Error())
			continue
		}
		n++
		s.appendEvent(ctx, l.jobID, "lease_expired", map[string]any{"attempt": l.attempt, "worker_id": l.worker})
	}
	return n
}

// sweepOrphans removes input/result directories that have no live job row.
// Only directories older than 10 minutes are touched, so an in-flight commit
// is never destroyed.
func (s *Service) sweepOrphans(ctx context.Context, rep *CleanupReport) int {
	removed := 0
	cutoff := nowUTC().Add(-10 * time.Minute)
	for _, sub := range []string{"inputs", "results"} {
		entries, err := os.ReadDir(filepath.Join(s.Store.Root, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			var status, retention string
			err = s.DB.QueryRowContext(ctx, `SELECT processing_status, retention_status FROM jobs WHERE id = ?`, e.Name()).
				Scan(&status, &retention)
			alive := err == nil && !jobs.RetentionStatus(retention).Terminal()
			if alive {
				continue
			}
			// A completed job without a DB row can only happen after the
			// tombstone purge, so the files are definitively orphaned.
			if err := os.RemoveAll(filepath.Join(s.Store.Root, sub, e.Name())); err == nil {
				removed++
				slog.Warn("removed orphan directory", "sub", sub, "job", e.Name())
			}
		}
	}
	return removed
}

// LastCleanup reports when the cleaner last completed a pass.
func (s *Service) LastCleanup() time.Time {
	v, _ := s.lastCleanup.Load().(time.Time)
	return v
}

// CleanupRuns returns the number of completed passes.
func (s *Service) CleanupRuns() int64 { return s.cleanupRuns.Load() }

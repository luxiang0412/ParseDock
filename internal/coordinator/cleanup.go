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
	"strings"
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
	rows, err := s.DB.QueryContext(ctx, `SELECT id, retention_status, retention_target FROM jobs
		WHERE retention_status IN ('release_pending','expiry_pending','deleting')`)
	if err != nil {
		rep.Errors = append(rep.Errors, "query pending: "+err.Error())
	} else {
		type pending struct{ id, status, target string }
		var list []pending
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.id, &p.status, &p.target); err == nil {
				list = append(list, p)
			}
		}
		_ = rows.Close()
		for _, p := range list {
			var busy int
			_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_leases WHERE job_id = ? AND julianday(expires_at) > julianday(?)`,
				p.id, ts(nowUTC())).Scan(&busy)
			if busy > 0 {
				rep.SkippedBusy++
				continue
			}
			// The intended final state is persisted when the job enters 'deleting',
			// so a retry after a crash cannot confuse released and expired.
			final := jobs.RetentionReleased
			switch {
			case jobs.RetentionStatus(p.status) == jobs.RetentionExpiryPending:
				final = jobs.RetentionExpired
			case jobs.RetentionStatus(p.status) == jobs.RetentionDeleting && p.target != "":
				final = jobs.RetentionStatus(p.target)
			}
			// Mark deleting first: a crash here is recovered because the
			// cleaner re-scans 'deleting' rows on the next pass. The lease check
			// is inside the UPDATE so a download that starts concurrently cannot
			// be deleted underneath (F03).
			res, err := s.DB.ExecContext(ctx, `UPDATE jobs SET retention_status = 'deleting',
				retention_target = ?, updated_at = ?
				WHERE id = ? AND retention_status IN ('release_pending','expiry_pending','deleting')
				  AND NOT EXISTS (SELECT 1 FROM download_leases
				                  WHERE job_id = ? AND julianday(expires_at) > julianday(?))`,
				string(final), ts(nowUTC()), p.id, p.id, ts(nowUTC()))
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
			// Final status, storage release and artifact rows move together: if this
			// transaction fails the job stays in 'deleting' and the next pass retries,
			// so the owner's balance can never be charged for deleted files.
			tomb := ts(nowUTC().Add(s.Cfg.TombstoneTTL))
			tx, terr := s.DB.BeginTx(ctx, nil)
			if terr != nil {
				rep.Errors = append(rep.Errors, "finalize: "+terr.Error())
				continue
			}
			var ownerID string
			var stored int64
			_ = tx.QueryRowContext(ctx, `SELECT owner_id FROM jobs WHERE id = ?`, p.id).Scan(&ownerID)
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0) FROM artifacts WHERE job_id = ?`, p.id).Scan(&stored)
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET retention_status = ?, tombstone_expires_at = ?,
				result_version = COALESCE(result_version, ''), updated_at = ? WHERE id = ?`,
				string(final), tomb, ts(nowUTC()), p.id); err != nil {
				_ = tx.Rollback()
				rep.Errors = append(rep.Errors, "finalize: "+err.Error())
				continue
			}
			if err := adjustStorage(ctx, tx, ownerID, -stored); err != nil {
				_ = tx.Rollback()
				rep.Errors = append(rep.Errors, "finalize accounting: "+err.Error())
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE job_id = ?`, p.id); err != nil {
				_ = tx.Rollback()
				rep.Errors = append(rep.Errors, "finalize artifacts: "+err.Error())
				continue
			}
			if err := tx.Commit(); err != nil {
				rep.Errors = append(rep.Errors, "finalize commit: "+err.Error())
				continue
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
	if len(rep.Errors) == 0 {
		s.instr().cleanupRuns.With("ok").Inc()
	} else {
		s.instr().cleanupRuns.With("error").Inc()
	}
	if rep.LeasesReaped > 0 {
		s.instr().expired.With().Add(uint64(rep.LeasesReaped))
	}
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
		WHERE state = 'running' AND julianday(lease_until) <= julianday(?)`, ts(now))
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
		// The candidate list was read outside this transaction: a heartbeat may have renewed
		// the lease in the meantime, and another cleanup pass may be racing this one. Expire
		// only a still-running, still-expired attempt, and require exactly one row.
		res, err := tx.ExecContext(ctx, `UPDATE attempts SET state = 'expired', error = 'lease expired'
			WHERE job_id = ? AND attempt_no = ? AND state = 'running' AND julianday(lease_until) <= julianday(?)`,
			l.jobID, l.attempt, ts(now))
		if err != nil {
			_ = tx.Rollback()
			rep.Errors = append(rep.Errors, "reap attempt: "+err.Error())
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			_ = tx.Rollback() // renewed or already reaped: nothing to do
			continue
		}
		if jobs.ProcessingStatus(status) == jobs.StatusRunning && attemptsMade < maxAttempts {
			backoff := time.Duration(1<<minInt(attemptsMade, 6)) * time.Second
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET processing_status = 'queued',
				progress = 0, progress_stage = 'requeued_after_lease_expiry', error_code = ?,
				error_message = 'worker lease expired', available_at = ?, updated_at = ?
				WHERE id = ? AND processing_status = 'running' AND current_attempt = ?`,
				jobs.ErrLeaseLost, ts(now.Add(backoff)), ts(now), l.jobID, l.attempt); err != nil {
				_ = tx.Rollback()
				rep.Errors = append(rep.Errors, "reap requeue: "+err.Error())
				continue
			}
		} else if jobs.ProcessingStatus(status) == jobs.StatusRunning {
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET processing_status = 'failed',
				error_code = ?, error_message = 'worker lease expired after max attempts',
				completed_at = ?, retention_status = 'release_pending', updated_at = ?
				WHERE id = ? AND processing_status = 'running' AND current_attempt = ?`,
				jobs.ErrLeaseLost, ts(now), ts(now), l.jobID, l.attempt); err != nil {
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
// committedResultVersion reads the version the database currently points at.
func (s *Service) committedResultVersion(jobID string) (string, error) {
	var v sql.NullString
	err := s.DB.QueryRowContext(context.Background(),
		`SELECT result_version FROM jobs WHERE id = ?`, jobID).Scan(&v)
	if err != nil {
		return "", err
	}
	return v.String, nil
}

// staleStageDirs lists publication staging directories older than the orphan cutoff.
func staleStageDirs(jobDir string) []string {
	entries, err := os.ReadDir(jobDir)
	if err != nil {
		return nil
	}
	cutoff := nowUTC().Add(-10 * time.Minute)
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".stage-") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			out = append(out, filepath.Join(jobDir, e.Name()))
		}
	}
	return out
}

// pruneGenerations removes result directories the database does not reference: a losing
// completion leaves one behind by design (deleting it synchronously could remove a
// byte-identical generation another attempt just committed), and a crash can leave a staging
// directory. Only entries older than the orphan cutoff are removed, so an in-flight
// publication is never touched. Returns how many were removed.
func (s *Service) pruneGenerations(jobDir, sub, keepVersion, jobID string) int {
	if sub != "results" {
		return 0
	}
	// The caller holds the job's publication lock. Re-read the committed version anyway: it
	// read it before listing the directory, and a completion may have republished into one of
	// these entries since.
	if v, err := s.committedResultVersion(jobID); err == nil {
		keepVersion = v
	}
	if leased, err := s.hasActiveDownloadLease(context.Background(), jobID); err == nil && leased {
		return 0 // a client or an online backup may be copying this job
	}
	entries, err := os.ReadDir(jobDir)
	if err != nil {
		return 0
	}
	keep := strings.TrimPrefix(keepVersion, "sha256:")
	cutoff := nowUTC().Add(-10 * time.Minute)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(jobDir, e.Name())); err == nil {
			removed++
		}
	}
	return removed
}

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
			// Serialise with publication for this job: Complete holds the same lock from the
			// first staged byte to the committed row, so nothing here can see a half-written
			// generation as abandoned.
			unlock := lockJob(e.Name())
			removed += s.sweepOne(ctx, rep, sub, e.Name(), cutoff)
			unlock()
		}
	}
	return removed
}

// sweepOne reclaims one job directory. The caller holds the job's publication lock.
func (s *Service) sweepOne(ctx context.Context, rep *CleanupReport, sub, jobID string, cutoff time.Time) int {
	dir := filepath.Join(s.Store.Root, sub, jobID)
	// Staging directories from a crashed publication are never served; with the lock held
	// none can be in flight.
	for _, stale := range staleStageDirs(dir) {
		_ = os.RemoveAll(stale)
		rep.StaleParts++
	}
	var status, retention, version string
	err := s.DB.QueryRowContext(ctx, `SELECT processing_status, retention_status, COALESCE(result_version,'')
		FROM jobs WHERE id = ?`, jobID).
		Scan(&status, &retention, &version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// The row is gone (tombstone purge), so the directory is definitively orphaned.
	case err != nil:
		// A failed lookup is not evidence of an orphan: a cancelled context during shutdown
		// makes every query fail, and deleting then would remove live data.
		rep.Errors = append(rep.Errors, "orphan scan: "+err.Error())
		return 0
	case !jobs.RetentionStatus(retention).Terminal():
		// Generations the database does not reference (a losing completion, an abandoned
		// attempt) are dead weight and are not charged to storage.
		return s.pruneGenerations(dir, sub, version, jobID)
	}
	if ctx.Err() != nil {
		return 0 // a cancelled pass must not delete anything
	}
	if err := os.RemoveAll(dir); err == nil {
		slog.Warn("removed orphan directory", "sub", sub, "job", jobID)
		return 1
	}
	return 0
}

// LastCleanup reports when the cleaner last completed a pass.
func (s *Service) LastCleanup() time.Time {
	v, _ := s.lastCleanup.Load().(time.Time)
	return v
}

// CleanupRuns returns the number of completed passes.
func (s *Service) CleanupRuns() int64 { return s.cleanupRuns.Load() }

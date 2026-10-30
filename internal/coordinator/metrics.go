package coordinator

import (
	"context"
	"net/http"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
	"parsedock.dev/parsedock/internal/metrics"
)

// Instruments is the coordinator's metric surface. Names are stable and documented in
// docs/SLO.md; adding a label with unbounded cardinality (owner, job id, path) is not
// allowed, because it turns the exposition into a memory leak.
type Instruments struct {
	Reg *metrics.Registry

	jobsCreated *metrics.CounterVec // {mode,format}
	claimWait   *metrics.HistogramVec
	parseTime   *metrics.HistogramVec // {engine}
	resultBytes *metrics.CounterVec   // {engine}
	throttled   *metrics.CounterVec   // {reason}
	cleanupRuns *metrics.CounterVec   // {outcome}
	expired     *metrics.CounterVec   // {}

	state     *metrics.GaugeVec // {state}
	retention *metrics.GaugeVec // {state}
	queue     *metrics.GaugeVec // {}
	diskFree  *metrics.GaugeVec // {}
	workers   *metrics.GaugeVec // {device}
	oldest    *metrics.GaugeVec // {} age of the oldest queued job, seconds
}

// NewInstruments registers the coordinator instruments on reg.
func NewInstruments(reg *metrics.Registry) *Instruments {
	if reg == nil {
		reg = metrics.Default
	}
	return &Instruments{
		Reg:         reg,
		jobsCreated: reg.Counter("parsedock_jobs_created_total", "Jobs accepted, by requested mode and detected format.", "mode", "format"),
		claimWait:   reg.Histogram("parsedock_claim_wait_seconds", "Time from job creation to the first claim."),
		parseTime:   reg.Histogram("parsedock_parse_seconds", "Time from claim to committed result, by engine.", "engine"),
		resultBytes: reg.Counter("parsedock_result_bytes_total", "Bytes of committed result artifacts, by engine.", "engine"),
		throttled:   reg.Counter("parsedock_throttled_total", "Requests refused by rate limiting or quota, by reason.", "reason"),
		cleanupRuns: reg.Counter("parsedock_cleanup_passes_total", "Cleanup passes, by outcome.", "outcome"),
		expired:     reg.Counter("parsedock_leases_expired_total", "Attempts whose lease expired (worker lost)."),
		state:       reg.Gauge("parsedock_jobs", "Jobs by processing status.", "state"),
		retention:   reg.Gauge("parsedock_retention", "Jobs by retention status.", "state"),
		queue:       reg.Gauge("parsedock_queue_depth", "Jobs waiting to be claimed."),
		diskFree:    reg.Gauge("parsedock_disk_free_bytes", "Free bytes on the coordinator data volume."),
		workers:     reg.Gauge("parsedock_workers_online", "Workers with a heartbeat in the last 3 minutes, by device.", "device"),
		oldest:      reg.Gauge("parsedock_oldest_queued_seconds", "Age of the oldest queued job, seconds (0 when idle)."),
	}
}

// defaultInstruments is used by the server so instrumentation never has to be threaded
// through every call site.
var defaultInstruments = NewInstruments(metrics.Default)

// MetricsHandler serves the Prometheus text exposition. It is guarded by the internal
// token along with the rest of /internal: metrics include queue depth and worker counts,
// which are operational data, not public data.
func (s *Server) MetricsHandler() http.HandlerFunc {
	return s.internalGuard(func(w http.ResponseWriter, r *http.Request) {
		if s.Svc.Instruments != nil {
			s.Svc.Instruments.refresh(r.Context(), s.Svc)
		} else {
			defaultInstruments.refresh(r.Context(), s.Svc)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		reg := metrics.Default
		if s.Svc.Instruments != nil {
			reg = s.Svc.Instruments.Reg
		}
		if err := reg.WriteText(w); err != nil {
			s.Log.Warn("metrics exposition failed", "err", err)
		}
	})
}

// refresh recomputes the database-derived gauges. Gauges are pulled at scrape time
// rather than pushed on every transition, so a single scrape always describes one
// consistent database state and no write path pays for metrics.
func (i *Instruments) refresh(ctx context.Context, svc *Service) {
	// Zero the known states first: a state whose last job left the table produces no
	// GROUP BY row, and without this the gauge would freeze at its previous value.
	for _, st := range []string{string(jobs.StatusQueued), string(jobs.StatusRunning),
		string(jobs.StatusCompleted), string(jobs.StatusFailed), string(jobs.StatusCancelled)} {
		i.state.With(st).Set(0)
	}
	for _, st := range []string{string(jobs.RetentionActive), string(jobs.RetentionReleasePending),
		string(jobs.RetentionExpiryPending), string(jobs.RetentionDeleting),
		string(jobs.RetentionReleased), string(jobs.RetentionExpired)} {
		i.retention.With(st).Set(0)
	}
	rows, err := svc.DB.QueryContext(ctx, `SELECT processing_status, COUNT(*) FROM jobs GROUP BY 1`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var state string
			var n float64
			if err := rows.Scan(&state, &n); err == nil {
				i.state.With(state).Set(n)
			}
		}
	}
	if rows, err := svc.DB.QueryContext(ctx, `SELECT retention_status, COUNT(*) FROM jobs GROUP BY 1`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var state string
			var n float64
			if err := rows.Scan(&state, &n); err == nil {
				i.retention.With(state).Set(n)
			}
		}
	}
	var queued float64
	var oldest string
	_ = svc.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(created_at),'') FROM jobs WHERE processing_status = 'queued'`).
		Scan(&queued, &oldest)
	i.queue.With().Set(queued)
	if oldest != "" {
		if t, err := parseTS(oldest); err == nil {
			i.oldest.With().Set(time.Since(t).Seconds())
		}
	} else {
		i.oldest.With().Set(0)
	}

	if free, err := svc.Store.FreeBytes(); err == nil {
		i.diskFree.With().Set(float64(free))
	}

	// Worker counts per device, using the same 3-minute liveness window as routing.
	if rows, err := svc.DB.QueryContext(ctx, `SELECT device_type, COUNT(*) FROM workers
		WHERE heartbeat_at > ? GROUP BY 1`, ts(nowUTC().Add(-3*time.Minute))); err == nil {
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var dev string
			var n float64
			if err := rows.Scan(&dev, &n); err == nil {
				i.workers.With(dev).Set(n)
				seen[dev] = true
			}
		}
		// Zero the series that disappeared so the gauge does not freeze at a stale count.
		for _, dev := range []string{"cpu", "gpu", "cuda", "mps"} {
			if !seen[dev] {
				i.workers.With(dev).Set(0)
			}
		}
	}
}

// throttleReason classifies a refusal so the counter stays low-cardinality (RC-5).
func throttleReason(code string) string {
	switch code {
	case "rate_limited":
		return "rate_limited"
	case "quota_exceeded":
		return "quota_exceeded"
	case "insufficient_storage":
		return "insufficient_storage"
	default:
		return ""
	}
}

func durSeconds(d time.Duration) float64 { return d.Seconds() }

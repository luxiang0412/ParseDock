package worker

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
)

// leaseState is how an attempt's lease ended, if it did.
type leaseState int32

const (
	leaseHeld      leaseState = iota // still ours (or the attempt finished normally)
	leaseCancelled                   // the coordinator cancelled the job
	leaseLost                        // the coordinator says the lease is gone, or it ran out while unreachable
)

// leaseKeeper renews one attempt's lease and decides when a failed heartbeat means the
// work has to stop.
//
// A single failed heartbeat used to cancel the parse and report the job as `cancelled`
// (non-retryable): a coordinator restart or a network blip killed healthy jobs. Now only
// an explicit answer from the coordinator (cancelled / lease_lost) stops the attempt at
// once. Any other failure (connection refused, timeout, 5xx) is retried on the next tick
// until the lease would have expired by the worker's own estimate; only then is the lease
// treated as lost.
type leaseKeeper struct {
	w           *Worker
	task        *coordClaim
	cancelParse context.CancelFunc

	progress atomic.Int64
	stage    atomic.Value // string
	state    atomic.Int32 // leaseState
	detail   atomic.Value // string: why the lease ended

	stopHB context.CancelFunc
	done   chan struct{}
}

// startLease starts the heartbeat loop for task. cancelParse is called when the lease
// ends, so the engine stops working for a job this worker no longer owns.
func (w *Worker) startLease(parseCtx context.Context, task *coordClaim, cancelParse context.CancelFunc) *leaseKeeper {
	lk := &leaseKeeper{w: w, task: task, cancelParse: cancelParse, done: make(chan struct{})}
	lk.stage.Store("downloading")
	hbCtx, stop := context.WithCancel(parseCtx)
	lk.stopHB = stop
	go lk.run(hbCtx)
	return lk
}

// stop ends the heartbeat loop and waits for it.
func (lk *leaseKeeper) stop() {
	lk.stopHB()
	<-lk.done
}

func (lk *leaseKeeper) setStage(s string) { lk.stage.Store(s) }

// report is the engine progress callback.
func (lk *leaseKeeper) report(p int, s string) {
	lk.progress.Store(int64(p))
	lk.stage.Store(s)
}

// ended returns how the lease ended (leaseHeld when it did not).
func (lk *leaseKeeper) ended() (leaseState, string) {
	st := leaseState(lk.state.Load())
	d, _ := lk.detail.Load().(string)
	return st, d
}

func (lk *leaseKeeper) end(st leaseState, detail string) {
	// Publish the reason before cancelling: whoever observes the cancelled parse context
	// must also observe why.
	lk.detail.Store(detail)
	lk.state.Store(int32(st))
	lk.cancelParse()
}

func (lk *leaseKeeper) run(ctx context.Context) {
	defer close(lk.done)
	expiry := lk.w.leaseExpiry(lk.task.LeaseUntil)
	t := time.NewTicker(lk.w.Cfg.HeartbeatInterval)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// Send on every tick, not only when progress changes: during download and
		// upload the stage does not change, and a skipped heartbeat is an expiring lease.
		st, _ := lk.stage.Load().(string)
		until, err := lk.w.heartbeat(ctx, lk.task, int(lk.progress.Load()), st)
		if ctx.Err() != nil {
			// The attempt is over (finished, deadline, shutdown). A heartbeat cut short by
			// that says nothing about the lease; reporting it was how a parse deadline used
			// to surface as `cancelled`.
			return
		}
		if err == nil {
			if failures > 0 {
				lk.w.Log.Info("heartbeat recovered", "job", lk.task.JobID, "failed_heartbeats", failures)
			}
			failures = 0
			expiry = lk.w.leaseExpiry(until)
			continue
		}
		switch errorCode(err) {
		case jobs.ErrCancelled:
			lk.end(leaseCancelled, err.Error())
			return
		case jobs.ErrLeaseLost:
			lk.end(leaseLost, err.Error())
			return
		}
		failures++
		if !time.Now().Before(expiry) {
			lk.end(leaseLost, fmt.Sprintf("no heartbeat accepted before the lease ran out (%d failed, last: %v)", failures, err))
			return
		}
		lk.w.Log.Warn("heartbeat failed; keeping the attempt until the lease would expire",
			"job", lk.task.JobID, "err", err, "failed_heartbeats", failures,
			"lease_left", time.Until(expiry).Round(time.Second).String())
	}
}

// leaseExpiry converts the coordinator's lease_until into a deadline on this worker's
// clock. The remaining duration is what matters; it is measured once, when the answer
// arrives, so a skewed clock shifts the estimate by the skew only at that moment. When the
// coordinator sent nothing usable (or the clocks disagree so much that the lease already
// looks expired) the worker assumes three heartbeat intervals, which is well inside the
// default 60 s lease for the default 10 s interval.
func (w *Worker) leaseExpiry(until time.Time) time.Time {
	remaining := time.Until(until)
	if until.IsZero() || remaining <= 0 {
		remaining = 3 * w.Cfg.HeartbeatInterval
	}
	return time.Now().Add(remaining)
}

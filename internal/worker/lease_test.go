package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/engines"
	"parsedock.dev/parsedock/internal/jobs"
)

// fakeCoord is the coordinator's internal task API, reduced to what one attempt uses.
type fakeCoord struct {
	t *testing.T
	// heartbeat answers the n-th heartbeat (1-based) with a status and a body.
	heartbeat func(n int) (int, string)

	mu       sync.Mutex
	hbCount  int
	fails    []map[string]any
	results  int
	register int
}

func newFakeCoord(t *testing.T, hb func(n int) (int, string)) (*fakeCoord, *httptest.Server) {
	fc := &fakeCoord{t: t, heartbeat: hb}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/tasks/{id}/input", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "%PDF-1.7\n%%EOF\n")
	})
	mux.HandleFunc("POST /internal/tasks/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		fc.mu.Lock()
		fc.hbCount++
		n := fc.hbCount
		fc.mu.Unlock()
		code, body := fc.heartbeat(n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("PUT /internal/tasks/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		fc.mu.Lock()
		fc.results++
		fc.mu.Unlock()
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("POST /internal/tasks/{id}/fail", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fc.mu.Lock()
		fc.fails = append(fc.fails, body)
		fc.mu.Unlock()
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("POST /internal/workers/register", func(w http.ResponseWriter, r *http.Request) {
		fc.mu.Lock()
		fc.register++
		fc.mu.Unlock()
		fmt.Fprint(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return fc, srv
}

func (fc *fakeCoord) snapshot() (hb, results int, fails []map[string]any) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.hbCount, fc.results, append([]map[string]any(nil), fc.fails...)
}

func okHeartbeat() (int, string) {
	return 200, fmt.Sprintf(`{"lease_until":%q}`, time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano))
}

func errEnvelope(code string) string {
	return fmt.Sprintf(`{"error":{"code":%q,"message":"x"}}`, code)
}

// ctxEngine works until its context ends, then reports the interruption the way the
// adapters do.
type ctxEngine struct{ plainErr bool }

func (ctxEngine) Name() string                { return "ctx-engine" }
func (ctxEngine) Version() string             { return "1" }
func (ctxEngine) Device() string              { return "cpu" }
func (ctxEngine) Formats() []string           { return []string{"pdf"} }
func (ctxEngine) Modes() []string             { return []string{"fast"} }
func (ctxEngine) Probe(context.Context) error { return nil }
func (e ctxEngine) Parse(ctx context.Context, _ engines.Request) (*documentir.Document, error) {
	<-ctx.Done()
	if e.plainErr {
		return nil, ctx.Err()
	}
	return nil, engines.Errf(true, jobs.ErrCancelled, "engine stopped because the parse was cancelled")
}

func leaseWorker(t *testing.T, srv *httptest.Server, eng engines.Engine, maxParse time.Duration) *Worker {
	t.Helper()
	return New(Config{
		CoordinatorURL: srv.URL, WorkDir: t.TempDir(),
		HeartbeatInterval: 30 * time.Millisecond, MaxParseTime: maxParse,
	}, engines.NewRegistry(eng), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func leaseTask(leaseFor time.Duration) *coordClaim {
	return &coordClaim{JobID: "job_LEASE", AttemptNo: 1, LeaseToken: "lt_x",
		LeaseUntil: time.Now().Add(leaseFor), Format: "pdf", Mode: "fast"}
}

// Review finding: one failed heartbeat cancelled a healthy parse and reported it as a
// non-retryable `cancelled`. A transient failure inside the lease must be survived.
func TestHeartbeatBlipDoesNotKillTheParse(t *testing.T) {
	fc, srv := newFakeCoord(t, func(n int) (int, string) {
		if n <= 2 {
			return 503, errEnvelope(jobs.ErrInternal) // coordinator restarting
		}
		return okHeartbeat()
	})
	eng := &stubEngine{name: "stub", modes: []string{"fast"}, text: "hello", delay: 400 * time.Millisecond}
	w := leaseWorker(t, srv, eng, time.Minute)
	if err := w.execute(context.Background(), leaseTask(time.Minute)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	hb, results, fails := fc.snapshot()
	if hb < 3 {
		t.Fatalf("only %d heartbeats; the blip was not followed by a successful one", hb)
	}
	if results != 1 || len(fails) != 0 {
		t.Fatalf("results=%d fails=%v, want 1 result and no failure", results, fails)
	}
}

// An explicit lease_lost stops the attempt at once, and nothing is reported: the
// coordinator requeues the job itself.
func TestHeartbeatLeaseLostStopsWithoutReporting(t *testing.T) {
	fc, srv := newFakeCoord(t, func(int) (int, string) { return 409, errEnvelope(jobs.ErrLeaseLost) })
	w := leaseWorker(t, srv, ctxEngine{}, time.Minute)
	start := time.Now()
	err := w.execute(context.Background(), leaseTask(time.Minute))
	if err == nil || !strings.Contains(err.Error(), jobs.ErrLeaseLost) {
		t.Fatalf("err = %v, want lease_lost", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("lease loss took %v to stop the parse", time.Since(start))
	}
	if _, results, fails := fc.snapshot(); results != 0 || len(fails) != 0 {
		t.Fatalf("results=%d fails=%v, want nothing reported", results, fails)
	}
}

func TestHeartbeatCancelledStopsCleanly(t *testing.T) {
	fc, srv := newFakeCoord(t, func(int) (int, string) { return 409, errEnvelope(jobs.ErrCancelled) })
	w := leaseWorker(t, srv, ctxEngine{}, time.Minute)
	if err := w.execute(context.Background(), leaseTask(time.Minute)); err != nil {
		t.Fatalf("err = %v, want a clean stop", err)
	}
	if _, results, fails := fc.snapshot(); results != 0 || len(fails) != 0 {
		t.Fatalf("results=%d fails=%v, want nothing reported", results, fails)
	}
}

// A coordinator that stays unreachable past the lease: the worker gives up once the lease
// would have expired, and does not post a misleading failure.
func TestUnreachableCoordinatorPastLeaseExpiry(t *testing.T) {
	fc, srv := newFakeCoord(t, func(int) (int, string) { return 503, "unavailable" })
	w := leaseWorker(t, srv, ctxEngine{}, time.Minute)
	start := time.Now()
	err := w.execute(context.Background(), leaseTask(300*time.Millisecond))
	if err == nil || !strings.Contains(err.Error(), jobs.ErrLeaseLost) {
		t.Fatalf("err = %v, want lease_lost", err)
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 5*time.Second {
		t.Fatalf("gave up after %v; want roughly the remaining lease", d)
	}
	if _, results, fails := fc.snapshot(); results != 0 || len(fails) != 0 {
		t.Fatalf("results=%d fails=%v, want nothing reported", results, fails)
	}
}

// Review finding: a real parse deadline surfaced as `cancelled` (the heartbeat in flight
// when the deadline hit was treated as a lease failure). It must be a timeout.
func TestParseDeadlineIsReportedAsTimeout(t *testing.T) {
	for _, plain := range []bool{false, true} {
		t.Run(fmt.Sprintf("plain_ctx_error=%v", plain), func(t *testing.T) {
			fc, srv := newFakeCoord(t, func(int) (int, string) { return okHeartbeat() })
			w := leaseWorker(t, srv, ctxEngine{plainErr: plain}, 250*time.Millisecond)
			if err := w.execute(context.Background(), leaseTask(time.Minute)); err == nil {
				t.Fatal("deadline exceeded but execute reported success")
			}
			_, results, fails := fc.snapshot()
			if results != 0 || len(fails) != 1 {
				t.Fatalf("results=%d fails=%v, want one failure", results, fails)
			}
			if fails[0]["code"] != jobs.ErrTimeout {
				t.Fatalf("reported %v, want %s", fails[0]["code"], jobs.ErrTimeout)
			}
		})
	}
}

// A worker shutting down hands the job back as retryable instead of blaming the file.
func TestWorkerShutdownIsRetryable(t *testing.T) {
	fc, srv := newFakeCoord(t, func(int) (int, string) { return okHeartbeat() })
	w := leaseWorker(t, srv, ctxEngine{}, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	_ = w.execute(ctx, leaseTask(time.Minute))
	_, _, fails := fc.snapshot()
	if len(fails) != 1 || fails[0]["retryable"] != true || fails[0]["code"] == jobs.ErrCancelled {
		t.Fatalf("fails = %v, want one retryable non-cancelled failure", fails)
	}
}

// Review finding: an idle GPU worker re-registered (nvidia-smi twice + an upsert) on every
// 250 ms poll.
func TestGPURegistrationIsThrottled(t *testing.T) {
	w := &Worker{Cfg: Config{DeviceType: "gpu", HeartbeatInterval: 10 * time.Second}}
	if w.registrationDue(300*time.Millisecond, false) {
		t.Fatal("idle GPU worker re-registers on every poll")
	}
	if !w.registrationDue(10*time.Second, false) {
		t.Fatal("GPU worker never refreshes while idle")
	}
	if !w.registrationDue(time.Millisecond, true) {
		t.Fatal("GPU worker does not refresh VRAM after a task")
	}
	cpu := &Worker{Cfg: Config{DeviceType: "cpu", HeartbeatInterval: 10 * time.Second}}
	if cpu.registrationDue(30*time.Second, true) || !cpu.registrationDue(61*time.Second, false) {
		t.Fatal("CPU re-registration cadence changed")
	}
}

// Review finding: the sweeper deleted any two-level directory older than an hour, so a
// --work-dir pointing at a model cache would lose it.
func TestSweepScratchOnlyTouchesAttemptDirs(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-3 * time.Hour)
	mk := func(parts ...string) string {
		dir := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	cache := mk("huggingface", "hub")
	models := mk("models", "a1")
	notAttempt := mk("job_01ABC", "models")
	attempt := mk("job_01ABC", "a2")
	n, err := SweepScratch(root, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d, want only the attempt dir", n)
	}
	for _, keep := range []string{cache, models, notAttempt} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s removed: %v", keep, err)
		}
	}
	if _, err := os.Stat(attempt); !os.IsNotExist(err) {
		t.Fatal("stale attempt dir survived")
	}
}

func TestExecuteRefusesUnsafeJobID(t *testing.T) {
	_, srv := newFakeCoord(t, func(int) (int, string) { return okHeartbeat() })
	w := leaseWorker(t, srv, ctxEngine{}, time.Minute)
	task := leaseTask(time.Minute)
	task.JobID = "../escape"
	if err := w.execute(context.Background(), task); err == nil {
		t.Fatal("unsafe job id accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(w.Cfg.WorkDir), "escape")); err == nil {
		t.Fatal("scratch directory created outside the work dir")
	}
}

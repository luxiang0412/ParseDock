package coordinator

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
	"parsedock.dev/parsedock/internal/storage"
)

func testService(t *testing.T, tweak func(*Config)) *Service {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	store := &storage.Store{Root: filepath.Join(dir, "data")}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.CleanupInterval = 10 * time.Millisecond
	cfg.Engines = []EngineSpec{
		{Name: "native", Device: "cpu", Priority: 100, Formats: []string{"txt", "csv"}, Modes: []string{"fast"}},
		{Name: "fast-engine", Device: "cpu", Priority: 50, Formats: []string{"pdf"}, Modes: []string{"fast"}},
		{Name: "accurate-engine", Device: "both", Priority: 60, Formats: []string{"pdf"}, Modes: []string{"accurate"}},
	}
	if tweak != nil {
		tweak(&cfg)
	}
	return NewService(db, store, cfg)
}

func create(t *testing.T, s *Service, name string, body []byte, p CreateParams) *jobs.Job {
	t.Helper()
	p.OwnerID = "owner-1"
	p.InputName = name
	p.Body = bytes.NewReader(body)
	job, _, err := s.CreateJob(context.Background(), p)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

func TestRouteModeSemantics(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()

	if eng, _, err := s.route(ctx, "pdf", jobs.ModeFast, jobs.DeviceAuto); err != nil || eng != "fast-engine" {
		t.Fatalf("fast pdf -> %q, %v", eng, err)
	}
	if eng, _, err := s.route(ctx, "pdf", jobs.ModeAccurate, jobs.DeviceAuto); err != nil || eng != "accurate-engine" {
		t.Fatalf("accurate pdf -> %q, %v", eng, err)
	}
	// mode=auto may use any engine, highest priority wins.
	if eng, _, err := s.route(ctx, "pdf", jobs.ModeAuto, jobs.DeviceAuto); err != nil || eng != "accurate-engine" {
		t.Fatalf("auto pdf -> %q, %v", eng, err)
	}
	// gpu_required without a registered GPU worker must fail explicitly.
	if _, _, err := s.route(ctx, "pdf", jobs.ModeAccurate, jobs.DeviceGPURequired); err == nil {
		t.Fatal("gpu_required accepted with no GPU worker")
	} else if AsError(err).Code != jobs.ErrGPUUnavailable {
		t.Fatalf("gpu_required error code = %s", AsError(err).Code)
	}
	// Unsupported format/mode combination.
	if _, _, err := s.route(ctx, "pdf", jobs.ModeFast, jobs.DeviceCPU); err != nil {
		t.Fatalf("cpu fast pdf should work: %v", err)
	}
	if _, _, err := s.route(ctx, "pptx", jobs.ModeFast, jobs.DeviceAuto); err == nil {
		t.Fatal("unsupported format accepted")
	}
}

func TestUploadValidation(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()

	// Empty upload.
	if _, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "o", InputName: "a.txt", Body: bytes.NewReader(nil)}); AsError(err).Code != jobs.ErrMalformed {
		t.Fatalf("empty upload error = %v", err)
	}
	// Extension/content mismatch.
	_, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "o", InputName: "a.pdf", Body: stringsReader("hello")})
	if AsError(err).Code != jobs.ErrUnsupportedType {
		t.Fatalf("mismatched type error = %v", err)
	}
	// Upload limit.
	small := testService(t, func(c *Config) { c.MaxUploadBytes = 4 })
	_, _, err = small.CreateJob(ctx, CreateParams{OwnerID: "o", InputName: "a.txt", Body: stringsReader("0123456789")})
	if AsError(err).Code != jobs.ErrTooLarge {
		t.Fatalf("limit error = %v", err)
	}
}

func TestIdempotencyKey(t *testing.T) {
	s := testService(t, nil)
	first := create(t, s, "a.txt", []byte("hello"), CreateParams{IdempotencyKey: "k1"})
	again, fresh, err := s.CreateJob(context.Background(), CreateParams{
		OwnerID: "owner-1", InputName: "a.txt", IdempotencyKey: "k1", Body: bytes.NewReader([]byte("hello")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fresh || again.ID != first.ID {
		t.Fatalf("idempotency replay created a new job: %+v", again)
	}
	// A different owner with the same key is a different job.
	other, _, err := s.CreateJob(context.Background(), CreateParams{
		OwnerID: "owner-2", InputName: "a.txt", IdempotencyKey: "k1", Body: bytes.NewReader([]byte("hello")),
	})
	if err != nil || other.ID == first.ID {
		t.Fatalf("cross-owner idempotency collision: %v %v", other, err)
	}
}

func TestClaimHeartbeatCompleteFencing(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello world"), CreateParams{})

	// A worker without the engine cannot claim.
	if task, err := s.Claim(ctx, WorkerCaps{WorkerID: "w0", Engines: []string{"other"}, Device: "cpu"}); err != nil || task != nil {
		t.Fatalf("claimed with wrong engine: %+v %v", task, err)
	}
	task, err := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	if err != nil || task == nil {
		t.Fatalf("claim failed: %v", err)
	}
	if task.JobID != job.ID || task.AttemptNo != 1 || task.LeaseToken == "" {
		t.Fatalf("bad task %+v", task)
	}
	// A second worker gets nothing (already leased).
	if again, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w2", Engines: []string{"native"}, Device: "cpu"}); again != nil {
		t.Fatal("job was leased twice")
	}
	// Heartbeat with a bogus token is rejected.
	if _, err := s.Heartbeat(ctx, job.ID, "lt_bogus", 10, "x"); AsError(err).Code != jobs.ErrLeaseLost {
		t.Fatalf("bogus heartbeat accepted: %v", err)
	}
	if _, err := s.Heartbeat(ctx, job.ID, task.LeaseToken, 50, "parsing"); err != nil {
		t.Fatalf("heartbeat failed: %v", err)
	}

	doc := documentir.New()
	doc.Elements = []documentir.Element{{ID: "el_1", Type: documentir.ElParagraph, Text: "hi"}}
	doc.Finalize()

	// Wrong token cannot complete.
	if _, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: "lt_bogus", Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte(`{}`)}); AsError(err).Code != jobs.ErrLeaseLost {
		t.Fatalf("complete with bogus token accepted: %v", err)
	}
	done, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte(`{}`), PageCount: 1, Warnings: 0})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if done.ProcessingStatus != jobs.StatusCompleted || done.ResultVersion == "" || done.ResultExpiresAt == nil {
		t.Fatalf("bad completed job %+v", done)
	}
	// Late completion from the same (now closed) attempt must be rejected.
	if _, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("x"), Text: []byte("x"), JSON: []byte("{}")}); err == nil {
		t.Fatal("replayed completion accepted")
	}
	// Result is downloadable and consistent.
	f, st, m, err := s.OpenResult(job.ID, "markdown")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if st.Size() == 0 || m.Version != done.ResultVersion {
		t.Fatalf("result manifest mismatch: %v %v", st.Size(), m)
	}
}

func TestAckAndExpiryLifecycle(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello world"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	done, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}

	// Mismatched version does not release.
	if _, err := s.Ack(ctx, job.ID, "owner-1", "sha256:wrong", "release"); AsError(err).Code != jobs.ErrConflict {
		t.Fatalf("stale ack accepted: %v", err)
	}
	// Foreign owner sees nothing.
	if _, err := s.GetJob(ctx, job.ID, "owner-2"); AsError(err).Code != jobs.ErrNotFound {
		t.Fatalf("cross-owner read: %v", err)
	}
	acked, err := s.Ack(ctx, job.ID, "owner-1", done.ResultVersion, "release")
	if err != nil || acked.RetentionStatus != jobs.RetentionReleasePending {
		t.Fatalf("ack: %v %+v", err, acked)
	}
	// Repeat ack is a no-op success.
	if again, err := s.Ack(ctx, job.ID, "owner-1", done.ResultVersion, "release"); err != nil || again.RetentionStatus != jobs.RetentionReleasePending {
		t.Fatalf("repeat ack: %v %+v", err, again)
	}
	// No new downloads after ack.
	if _, _, _, err := s.BeginDownload(ctx, job.ID, "owner-1"); AsError(err).Code != jobs.ErrGone {
		t.Fatalf("download after ack: %v", err)
	}
	// Cleaner releases the files.
	rep := s.RunCleanup(ctx)
	if rep.Released != 1 {
		t.Fatalf("cleanup released %d jobs: %+v", rep.Released, rep)
	}
	after, _ := s.GetJob(ctx, job.ID, "owner-1")
	if after.RetentionStatus != jobs.RetentionReleased {
		t.Fatalf("retention = %s", after.RetentionStatus)
	}
	if files := s.Store.ResultDir(job.ID); dirExists(files) {
		t.Fatalf("result dir still exists: %s", files)
	}
	// Repeated cleanup is safe.
	s.RunCleanup(ctx)
	s.RunCleanup(ctx)
}

func TestDownloadLeaseBlocksCleanup(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello world"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	done, _ := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte("{}")})
	if _, err := s.Ack(ctx, job.ID, "owner-1", done.ResultVersion, "release"); err != nil {
		t.Fatal(err)
	}
	_, leaseID, _, err := s.BeginDownload(ctx, job.ID, "owner-1")
	_ = leaseID
	if err == nil {
		t.Fatal("download lease was granted after ack")
	}

	// Lease taken before the ACK blocks deletion.
	job2 := create(t, s, "b.txt", []byte("hello world"), CreateParams{})
	task2, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	done2, _ := s.Complete(ctx, CompleteParams{JobID: job2.ID, Token: task2.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte("{}")})
	_, lease2, _, err := s.BeginDownload(ctx, job2.ID, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ack(ctx, job2.ID, "owner-1", done2.ResultVersion, "release"); err != nil {
		t.Fatal(err)
	}
	rep := s.RunCleanup(ctx)
	if rep.SkippedBusy == 0 {
		t.Fatalf("cleaner did not skip a busy job: %+v", rep)
	}
	if !dirExists(s.Store.ResultDir(job2.ID)) {
		t.Fatal("files deleted while a download lease was active")
	}
	s.EndDownload(ctx, lease2)
	s.RunCleanup(ctx)
	if dirExists(s.Store.ResultDir(job2.ID)) {
		t.Fatal("files survived after the lease ended")
	}
}

func TestLeaseReaperRequeues(t *testing.T) {
	s := testService(t, func(c *Config) { c.WorkerLeaseTTL = time.Millisecond })
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello world"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	time.Sleep(5 * time.Millisecond)
	rep := s.RunCleanup(ctx)
	if rep.LeasesReaped != 1 {
		t.Fatalf("reaped %d leases: %+v", rep.LeasesReaped, rep)
	}
	requeued, _ := s.GetJob(ctx, job.ID, "")
	if requeued.ProcessingStatus != jobs.StatusQueued {
		t.Fatalf("job status after reap = %s", requeued.ProcessingStatus)
	}
	// The stale token is dead.
	if _, err := s.Heartbeat(ctx, job.ID, task.LeaseToken, 10, "x"); AsError(err).Code != jobs.ErrLeaseLost {
		t.Fatalf("stale heartbeat accepted: %v", err)
	}
}

func TestMaxLifetimeFailsQueuedJob(t *testing.T) {
	s := testService(t, func(c *Config) { c.MaxJobLifetime = time.Millisecond })
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	time.Sleep(5 * time.Millisecond)
	s.RunCleanup(ctx)
	got, _ := s.GetJob(ctx, job.ID, "")
	if got.ProcessingStatus != jobs.StatusFailed || got.ErrorCode != jobs.ErrTimeout {
		t.Fatalf("job after lifetime expiry = %s/%s", got.ProcessingStatus, got.ErrorCode)
	}
}

func TestEventStreamOrdering(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello world"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	if _, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.Events(ctx, job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) < 3 {
		t.Fatalf("expected created/running/completed, got %d", len(evs))
	}
	want := []string{"created", "running", "completed"}
	for i, w := range want {
		if evs[i].Type != w {
			t.Fatalf("event %d = %s want %s", i, evs[i].Type, w)
		}
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].ID <= evs[i-1].ID {
			t.Fatal("event ids not increasing")
		}
	}
	// last-event-id resumption
	tail, _ := s.Events(ctx, job.ID, evs[1].ID, 100)
	if len(tail) == 0 || tail[0].ID != evs[2].ID {
		t.Fatalf("resume returned %+v", tail)
	}
}

func TestCancelIsIdempotent(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello world"), CreateParams{})
	first, err := s.Cancel(ctx, job.ID, "owner-1", "")
	if err != nil || first.ProcessingStatus != jobs.StatusCancelled {
		t.Fatalf("cancel: %v %+v", err, first)
	}
	second, err := s.Cancel(ctx, job.ID, "owner-1", "")
	if err != nil || second.ProcessingStatus != jobs.StatusCancelled {
		t.Fatalf("repeat cancel: %v %+v", err, second)
	}
	if _, err := s.Cancel(ctx, job.ID, "owner-2", ""); AsError(err).Code != jobs.ErrNotFound {
		t.Fatalf("cross-owner cancel: %v", err)
	}
}

// F03: cancelling a completed job must not bypass the ACK version check.
func TestCancelCompletedRequiresResultVersion(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello world"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	done, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Cancel(ctx, job.ID, "owner-1", ""); AsError(err).Code != jobs.ErrConflict {
		t.Fatalf("cancel without result_version = %v, want conflict", err)
	}
	if _, err := s.Cancel(ctx, job.ID, "owner-1", "sha256:nope"); AsError(err).Code != jobs.ErrConflict {
		t.Fatalf("cancel with stale result_version = %v, want conflict", err)
	}
	still, _ := s.GetJob(ctx, job.ID, "owner-1")
	if still.RetentionStatus != jobs.RetentionActive {
		t.Fatalf("failed cancel changed retention to %s", still.RetentionStatus)
	}
	// With the correct version it releases exactly like ACK.
	released, err := s.Cancel(ctx, job.ID, "owner-1", done.ResultVersion)
	if err != nil || released.RetentionStatus != jobs.RetentionReleasePending {
		t.Fatalf("cancel with version: %v %+v", err, released)
	}
}

// F03: a cancelled worker can finalise cleanly (heartbeat says cancelled, fail is
// an idempotent no-op) instead of being told the lease was lost.
func TestCancelledAttemptFinalisation(t *testing.T) {
	s := testService(t, func(c *Config) { c.WorkerLeaseTTL = time.Minute })
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello world"), CreateParams{})
	task, err := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.Cancel(ctx, job.ID, "owner-1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(ctx, job.ID, task.LeaseToken, 10, "x"); AsError(err).Code != jobs.ErrCancelled {
		t.Fatalf("heartbeat after cancel = %v, want cancelled", err)
	}
	got, err := s.Fail(ctx, job.ID, task.LeaseToken, jobs.ErrCancelled, "worker stopped", false)
	if err != nil {
		t.Fatalf("fail after cancel should be an idempotent no-op, got %v", err)
	}
	if got.ProcessingStatus != jobs.StatusCancelled {
		t.Fatalf("job status changed to %s", got.ProcessingStatus)
	}
}

// F03: ACK and a new download cannot interleave into a deleted-but-leased state.
func TestDownloadLeaseIsAtomicWithRetention(t *testing.T) {
	s := testService(t, func(c *Config) { c.DownloadLeaseTTL = time.Minute })
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello world"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	done, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var leases, gone int
	var mu sync.Mutex
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i == 8 {
				_, _ = s.Ack(ctx, job.ID, "owner-1", done.ResultVersion, "release")
				return
			}
			_, lease, _, err := s.BeginDownload(ctx, job.ID, "owner-1")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				leases++
				s.EndDownload(ctx, lease)
			} else if AsError(err).Code == jobs.ErrGone {
				gone++
			}
		}(i)
	}
	wg.Wait()
	if leases+gone != 15 {
		t.Fatalf("leases=%d gone=%d, want 15 accounted for", leases, gone)
	}
	// Whatever the interleaving, a released job never keeps a lease behind.
	var live int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_leases WHERE job_id = ? AND expires_at > ?`,
		job.ID, ts(nowUTC())).Scan(&live)
	if live != 0 {
		t.Fatalf("%d download leases survived their own EndDownload", live)
	}
	rep := s.RunCleanup(ctx)
	if rep.Released != 1 {
		t.Fatalf("cleanup released %d: %+v", rep.Released, rep)
	}
}

// helpers ------------------------------------------------------------------

func stringsReader(s string) *bytes.Reader { return bytes.NewReader([]byte(s)) }

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// F02: routing is a policy, not a quality promise. Table-driven so every
// format × mode × device combination is pinned.
func TestRouteTable(t *testing.T) {
	s := testService(t, func(c *Config) {
		c.Engines = []EngineSpec{
			{Name: "native", Device: "cpu", Priority: 100, Formats: []string{"txt", "md", "csv", "tsv"}, Modes: []string{"fast", "accurate"}},
			{Name: "xberg", Device: "cpu", Priority: 50, Formats: []string{"pdf", "docx", "xlsx", "png"}, Modes: []string{"fast", "auto"}},
			{Name: "docling-rs", Device: "cpu", Priority: 40, Formats: []string{"pdf", "docx", "xlsx", "png"}, Modes: []string{"accurate"}},
			// Lower priority than the CPU engines on purpose: it must only win
			// when the caller asks for a GPU (gpu_preferred/gpu_required).
			{Name: "gpu-engine", Device: "gpu", Priority: 20, Formats: []string{"pdf"}, Modes: []string{"accurate"}},
		}
	})
	ctx := context.Background()
	cases := []struct {
		format, mode, device string
		want                 string // "" means error
		code                 string
	}{
		// text formats: native in every mode, including accurate
		{"csv", jobs.ModeAuto, jobs.DeviceAuto, "native", ""},
		{"csv", jobs.ModeFast, jobs.DeviceAuto, "native", ""},
		{"csv", jobs.ModeAccurate, jobs.DeviceAuto, "native", ""},
		{"csv", jobs.ModeAccurate, jobs.DeviceCPU, "native", ""},
		{"md", jobs.ModeAccurate, jobs.DeviceAuto, "native", ""},
		{"txt", jobs.ModeFast, jobs.DeviceAuto, "native", ""},
		{"tsv", jobs.ModeAccurate, jobs.DeviceAuto, "native", ""},
		// pdf: fast->xberg, accurate->docling-rs, auto->highest priority
		{"pdf", jobs.ModeFast, jobs.DeviceAuto, "xberg", ""},
		{"pdf", jobs.ModeAccurate, jobs.DeviceAuto, "docling-rs", ""},
		{"pdf", jobs.ModeAuto, jobs.DeviceAuto, "xberg", ""},
		// device constraints
		{"pdf", jobs.ModeFast, jobs.DeviceCPU, "xberg", ""},
		{"pdf", jobs.ModeAccurate, jobs.DeviceGPUPreferred, "gpu-engine", ""},
		// mode=fast must not be served by an accurate-only engine, and vice versa
		{"png", jobs.ModeAccurate, jobs.DeviceAuto, "docling-rs", ""},
		{"png", jobs.ModeFast, jobs.DeviceAuto, "xberg", ""},
		// unknown format
		{"pptx", jobs.ModeFast, jobs.DeviceAuto, "", jobs.ErrUnsupportedType},
		// gpu_required without a registered GPU worker is an explicit failure
		{"pdf", jobs.ModeAccurate, jobs.DeviceGPURequired, "", jobs.ErrGPUUnavailable},
	}
	for _, c := range cases {
		t.Run(c.format+"/"+c.mode+"/"+c.device, func(t *testing.T) {
			engine, reason, err := s.route(ctx, c.format, c.mode, c.device)
			if c.want == "" {
				if err == nil || AsError(err).Code != c.code {
					t.Fatalf("want error %s, got engine=%q err=%v", c.code, engine, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if engine != c.want {
				t.Fatalf("engine = %q, want %q (reason: %s)", engine, c.want, reason)
			}
			if reason == "" {
				t.Fatal("routing reason must always be recorded")
			}
		})
	}
}

// F02 regression: GB18030 must decode correctly in auto/fast/accurate.
func TestTextFormatsUseNativeInEveryMode(t *testing.T) {
	s := testService(t, func(c *Config) {
		c.Engines = []EngineSpec{
			{Name: "native", Device: "cpu", Priority: 100, Formats: []string{"txt", "md", "csv", "tsv"}, Modes: []string{"fast", "accurate"}},
			{Name: "xberg", Device: "cpu", Priority: 50, Formats: []string{"pdf", "csv", "txt"}, Modes: []string{"fast", "auto"}},
		}
	})
	ctx := context.Background()
	job, _, err := s.CreateJob(ctx, CreateParams{
		OwnerID: "owner-1", InputName: "gb.csv", Mode: jobs.ModeAccurate,
		Body: bytes.NewReader([]byte{0xC7, 0xF8, 0xD3, 0xF2, ',', 'a', '\n'}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.EngineHint != "native" {
		t.Fatalf("engine hint = %q, want native (reason: %s)", job.EngineHint, job.RoutingReason)
	}
	if !strings.Contains(job.RoutingReason, "native") {
		t.Fatalf("routing reason does not explain the choice: %s", job.RoutingReason)
	}
}

// F04: a coordinator bound to a non-loopback address must not run without a
// shared internal token, because it trusts the owner header from its caller.
func TestValidateInternalBinding(t *testing.T) {
	cases := []struct {
		addr, token string
		dev         bool
		wantErr     bool
	}{
		{"127.0.0.1:8081", "", false, false},
		{"localhost:8081", "", false, false},
		{":8081", "", false, true},
		{"0.0.0.0:8081", "", false, true},
		{"10.0.0.5:8081", "", false, true},
		{"0.0.0.0:8081", "secret", false, false},
		{"0.0.0.0:8081", "", true, false},
		{"[::1]:8081", "", false, false},
	}
	for _, c := range cases {
		err := ValidateInternalBinding(c.addr, c.token, c.dev)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateInternalBinding(%q, token=%q, dev=%v) = %v, wantErr=%v",
				c.addr, c.token, c.dev, err, c.wantErr)
		}
	}
}

// PR-02: quotas are enforced by the coordinator (the only stateful component),
// so they hold across any number of stateless API replicas.
func TestQuotaConcurrency(t *testing.T) {
	s := testService(t, func(c *Config) { c.Quota = Quota{MaxConcurrent: 2} })
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		create(t, s, "a.txt", []byte("hello"), CreateParams{})
	}
	_, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "owner-1", InputName: "c.txt",
		Body: bytes.NewReader([]byte("hello"))})
	ce := AsError(err)
	if ce.Code != jobs.ErrQuotaExceeded || ce.Status != 429 {
		t.Fatalf("third job = %v (status %d), want 429 quota", err, ce.Status)
	}
	if ce.RetryAfter <= 0 {
		t.Fatal("quota errors must suggest a retry delay")
	}
	// Completing a job frees the slot.
	task, err := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.Complete(ctx, CompleteParams{JobID: task.JobID, Token: task.LeaseToken,
		Engine: "native", Version: "1", Markdown: []byte("x"), Text: []byte("x"), JSON: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "owner-1", InputName: "c.txt",
		Body: bytes.NewReader([]byte("hello"))}); err != nil {
		t.Fatalf("job after completion rejected: %v", err)
	}
}

func TestQuotaDailyJobsAndBytes(t *testing.T) {
	s := testService(t, func(c *Config) { c.Quota = Quota{MaxJobsPerDay: 2} })
	ctx := context.Background()
	create(t, s, "a.txt", []byte("hello"), CreateParams{})
	create(t, s, "b.txt", []byte("hello"), CreateParams{})
	if _, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "owner-1", InputName: "c.txt",
		Body: bytes.NewReader([]byte("hello"))}); AsError(err).Code != jobs.ErrQuotaExceeded {
		t.Fatalf("daily job quota not enforced: %v", err)
	}

	b := testService(t, func(c *Config) { c.Quota = Quota{MaxBytesPerDay: 10} })
	create(t, b, "a.txt", []byte("12345"), CreateParams{})
	if _, _, err := b.CreateJob(ctx, CreateParams{OwnerID: "owner-1", InputName: "b.txt",
		Body: bytes.NewReader([]byte("123456"))}); AsError(err).Code != jobs.ErrQuotaExceeded {
		t.Fatalf("daily byte quota not enforced: %v", err)
	}

	// Zero means unlimited.
	u := testService(t, func(c *Config) { c.Quota = Quota{} })
	for i := 0; i < 5; i++ {
		create(t, u, "a.txt", []byte("hello"), CreateParams{})
	}
}

// Quotas are per owner, not global.
func TestQuotaIsPerOwner(t *testing.T) {
	s := testService(t, func(c *Config) { c.Quota = Quota{MaxConcurrent: 1} })
	ctx := context.Background()
	create(t, s, "a.txt", []byte("hello"), CreateParams{})
	if _, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "owner-2", InputName: "b.txt",
		Body: bytes.NewReader([]byte("hello"))}); err != nil {
		t.Fatalf("other owner blocked by quota: %v", err)
	}
}

// PR-02: audit lines carry identity and decisions, never document content.
func TestAuditLinesContainNoContent(t *testing.T) {
	var buf bytes.Buffer
	s := testService(t, nil)
	s.Audit = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := WithRequestID(context.Background(), "req_test123")
	secret := "SENTINEL-DOCUMENT-TEXT-0f2a"
	job, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "owner-1", InputName: "a.txt",
		Body: bytes.NewReader([]byte(secret))})
	if err != nil {
		t.Fatal(err)
	}
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	done, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# x"), Text: []byte("x"), JSON: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ack(ctx, job.ID, "owner-1", done.ResultVersion, "release"); err != nil {
		t.Fatal(err)
	}
	s.RunCleanup(ctx)

	out := buf.String()
	for _, want := range []string{`"audit":true`, `"event":"job_created"`, `"event":"job_acked"`,
		`"event":"job_released"`, `"job_id":"` + job.ID + `"`, `"request_id":"req_test123"`,
		`"owner_id":"owner-1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log missing %s\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) {
		t.Fatalf("audit log leaked document content:\n%s", out)
	}
}

// Codex review (PR-02): concurrent admission must not overshoot the quota.
func TestQuotaAdmissionIsAtomic(t *testing.T) {
	s := testService(t, func(c *Config) { c.Quota = Quota{MaxConcurrent: 3} })
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, quota int
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "owner-1", InputName: "a.txt",
				Body: bytes.NewReader([]byte("hello"))})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if AsError(err).Code == jobs.ErrQuotaExceeded {
				quota++
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 {
		t.Fatalf("%d jobs admitted with a limit of 3 (quota rejections: %d)", ok, quota)
	}
	var live int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE owner_id='owner-1'`).Scan(&live)
	if live != 3 {
		t.Fatalf("%d job rows persisted, want 3", live)
	}
}

// Codex review (PR-02): purging job tombstones must not restore the daily budget.
func TestQuotaUsageSurvivesTombstonePurge(t *testing.T) {
	s := testService(t, func(c *Config) {
		c.Quota = Quota{MaxJobsPerDay: 2}
		c.TombstoneTTL = time.Millisecond
	})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
		task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
		done, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken,
			Engine: "native", Version: "1", Markdown: []byte("x"), Text: []byte("x"), JSON: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Ack(ctx, job.ID, "owner-1", done.ResultVersion, "release"); err != nil {
			t.Fatal(err)
		}
	}
	// Cleanup removes the artifacts, then the tombstone rows themselves.
	time.Sleep(5 * time.Millisecond)
	for i := 0; i < 3; i++ {
		s.RunCleanup(ctx)
	}
	var jobRows int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE owner_id='owner-1'`).Scan(&jobRows)
	if jobRows != 0 {
		t.Fatalf("%d job rows remain; the test would not prove anything", jobRows)
	}
	if _, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "owner-1", InputName: "c.txt",
		Body: bytes.NewReader([]byte("hello"))}); AsError(err).Code != jobs.ErrQuotaExceeded {
		t.Fatalf("daily quota forgotten after tombstone purge: %v", err)
	}
}

// Codex review (PR-02): an old still-running job counts against concurrency, not
// against today's jobs/bytes allowance.
func TestQuotaOldRunningJobDoesNotConsumeDailyBudget(t *testing.T) {
	s := testService(t, func(c *Config) { c.Quota = Quota{MaxJobsPerDay: 2, MaxConcurrent: 5} })
	ctx := context.Background()
	old := create(t, s, "old.txt", []byte("hello"), CreateParams{})
	// Backdate the job and its usage row by 25 h: outside the rolling window but
	// still queued/running.
	past := ts(nowUTC().Add(-25 * time.Hour))
	if _, err := s.DB.ExecContext(ctx, `UPDATE jobs SET created_at=? WHERE id=?`, past, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE owner_usage SET created_at=? WHERE job_id=?`, past, old.ID); err != nil {
		t.Fatal(err)
	}
	u, err := s.ownerUsage(ctx, "owner-1", nowUTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if u.concurrent != 1 {
		t.Fatalf("concurrent = %d, want 1", u.concurrent)
	}
	if u.jobsToday != 0 || u.bytesToday != 0 {
		t.Fatalf("old job consumed the daily budget: %+v", u)
	}
	create(t, s, "a.txt", []byte("hello"), CreateParams{})
	create(t, s, "b.txt", []byte("hello"), CreateParams{})
}

// PR-03: a cancellation records when it was requested and the job never yields a
// result afterwards.
func TestCancelRecordsRequestTime(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	cancelled, err := s.Cancel(ctx, job.ID, "owner-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.ProcessingStatus != jobs.StatusCancelled {
		t.Fatalf("status = %s", cancelled.ProcessingStatus)
	}
	if cancelled.CancelRequestedAt == nil {
		t.Fatal("cancel_requested_at not recorded")
	}
	read, _ := s.GetJob(ctx, job.ID, "owner-1")
	if read.CancelRequestedAt == nil {
		t.Fatal("cancel_requested_at not persisted")
	}
	// A repeated cancel keeps the original timestamp (idempotent).
	first := *read.CancelRequestedAt
	again, err := s.Cancel(ctx, job.ID, "owner-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.CancelRequestedAt == nil || !again.CancelRequestedAt.Equal(first) {
		t.Fatalf("repeat cancel changed the request time: %v vs %v", again.CancelRequestedAt, first)
	}
}

// PR-06: live-result storage is accounted per owner and enforced.
func TestStorageQuota(t *testing.T) {
	s := testService(t, func(c *Config) { c.Quota = Quota{MaxStorageBytes: 400} })
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	// Artifacts are small (a few hundred bytes across md/txt/json).
	if _, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	var stored int64
	_ = s.DB.QueryRowContext(ctx, `SELECT bytes FROM owner_storage WHERE owner_id='owner-1'`).Scan(&stored)
	if stored <= 0 {
		t.Fatalf("storage accounting empty after completion: %d", stored)
	}
	// A second upload that would exceed the quota is rejected.
	big := bytes.Repeat([]byte("x"), 500)
	if _, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "owner-1", InputName: "b.txt",
		Body: bytes.NewReader(big)}); AsError(err).Code != jobs.ErrQuotaExceeded {
		t.Fatalf("storage quota not enforced: %v", err)
	}
	// Releasing the first result frees the accounting (after cleanup).
	done, _ := s.GetJob(ctx, job.ID, "owner-1")
	if _, err := s.Ack(ctx, job.ID, "owner-1", done.ResultVersion, "release"); err != nil {
		t.Fatal(err)
	}
	s.RunCleanup(ctx)
	_ = s.DB.QueryRowContext(ctx, `SELECT bytes FROM owner_storage WHERE owner_id='owner-1'`).Scan(&stored)
	if stored != 0 {
		t.Fatalf("storage accounting not released: %d", stored)
	}
}

// Codex review (PR-06): admission cannot bound result bytes, so completion must
// enforce the storage quota transactionally.
func TestStorageQuotaEnforcedAtCompletion(t *testing.T) {
	s := testService(t, func(c *Config) { c.Quota = Quota{MaxStorageBytes: 50} })
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	big := bytes.Repeat([]byte("x"), 500)
	_, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: big, Text: big, JSON: big})
	if AsError(err).Code != jobs.ErrQuotaExceeded {
		t.Fatalf("oversized result accepted: %v", err)
	}
	// No artifacts and no accounting may remain.
	var stored int64
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes),0) FROM owner_storage`).Scan(&stored)
	if stored != 0 {
		t.Fatalf("storage accounted for a rejected result: %d", stored)
	}
	if files := s.storedFilesForTest(job.ID); len(files) != 0 {
		t.Fatalf("rejected result left files: %v", files)
	}
}

// gzip quality values: gzip;q=0 means "do not gzip".
func TestAcceptsGzip(t *testing.T) {
	cases := map[string]bool{
		"":                   false,
		"gzip":               true,
		"deflate, gzip":      true,
		"gzip;q=0":           false,
		"gzip; q=0.0, br":    false,
		"br;q=1, gzip;q=0.5": true,
		"identity":           false,
	}
	for header, want := range cases {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

// storedFilesForTest lists artifact paths for a job (test helper).
func (s *Service) storedFilesForTest(jobID string) []string {
	var out []string
	for _, dir := range []string{s.Store.InputDir(jobID), s.Store.ResultDir(jobID)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// Completions rejected by the storage quota must be terminal: the job must not
// stay running (its input is gone) and the attempt must be closed.
func TestStorageQuotaRejectionIsTerminal(t *testing.T) {
	s := testService(t, func(c *Config) { c.Quota = Quota{MaxStorageBytes: 50} })
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	big := bytes.Repeat([]byte("x"), 500)
	if _, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: big, Text: big, JSON: big}); AsError(err).Code != jobs.ErrQuotaExceeded {
		t.Fatalf("want quota rejection, got %v", err)
	}
	got, err := s.GetJob(ctx, job.ID, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProcessingStatus != jobs.StatusFailed || got.ErrorCode != jobs.ErrQuotaExceeded {
		t.Fatalf("job not failed terminally: %s/%s", got.ProcessingStatus, got.ErrorCode)
	}
	if got.RetentionStatus != jobs.RetentionReleasePending {
		t.Fatalf("rejected job not scheduled for cleanup: %s", got.RetentionStatus)
	}
	// The attempt is closed, so the reaper will not requeue it.
	var state string
	_ = s.DB.QueryRowContext(ctx, `SELECT state FROM attempts WHERE job_id = ?`, job.ID).Scan(&state)
	if state != jobs.AttemptFailed {
		t.Fatalf("attempt state = %s", state)
	}
	// Cleanup releases it without leaving storage charged.
	if rep := s.RunCleanup(ctx); rep.Released == 0 {
		t.Fatalf("rejected job not released: %+v", rep)
	}
	var stored int64
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes),0) FROM owner_storage`).Scan(&stored)
	if stored != 0 {
		t.Fatalf("storage accounting leaked: %d", stored)
	}
}

// Codex review (PR-06): a crash after the files are gone but before finalization
// must be retried; 'deleting' jobs are therefore picked up again.
func TestCleanupRetriesDeletingJobs(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	task, _ := s.Claim(ctx, WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	done, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ack(ctx, job.ID, "owner-1", done.ResultVersion, "release"); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the files were deleted and the row entered 'deleting'.
	if err := s.Store.DeleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE jobs SET retention_status='deleting', retention_target='released' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	s.RunCleanup(ctx)
	got, _ := s.GetJob(ctx, job.ID, "owner-1")
	if got.RetentionStatus != jobs.RetentionReleased {
		t.Fatalf("deleting job not retried: %s", got.RetentionStatus)
	}
	var stored int64
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes),0) FROM owner_storage`).Scan(&stored)
	if stored != 0 {
		t.Fatalf("storage accounting leaked: %d", stored)
	}
}

package coordinator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
	first, err := s.Cancel(ctx, job.ID, "owner-1")
	if err != nil || first.ProcessingStatus != jobs.StatusCancelled {
		t.Fatalf("cancel: %v %+v", err, first)
	}
	second, err := s.Cancel(ctx, job.ID, "owner-1")
	if err != nil || second.ProcessingStatus != jobs.StatusCancelled {
		t.Fatalf("repeat cancel: %v %+v", err, second)
	}
	if _, err := s.Cancel(ctx, job.ID, "owner-2"); AsError(err).Code != jobs.ErrNotFound {
		t.Fatalf("cross-owner cancel: %v", err)
	}
}

// helpers ------------------------------------------------------------------

func stringsReader(s string) *bytes.Reader { return bytes.NewReader([]byte(s)) }

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

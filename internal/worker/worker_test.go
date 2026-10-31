package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"parsedock.dev/parsedock/internal/jobs"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/engines"
)

// F09: stale attempt directories are reclaimed; live ones are not.
func TestSweepScratch(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)

	mk := func(job, attempt string, mtime time.Time) string {
		dir := filepath.Join(root, job, attempt)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "input.pdf"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	stale := mk("job_old", "a1", old)
	fresh := mk("job_new", "a1", time.Now())
	liveOld := mk("job_live", "a1", old)

	active := func(dir string) bool { return dir == liveOld }

	n, err := SweepScratch(root, time.Hour, active)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d dirs, want 1", n)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale attempt survived")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh attempt was removed")
	}
	if _, err := os.Stat(liveOld); err != nil {
		t.Fatal("a directory in use was removed")
	}

	// A missing root is not an error (first start on a fresh host).
	if n, err := SweepScratch(filepath.Join(root, "nope"), time.Hour, nil); err != nil || n != 0 {
		t.Fatalf("missing root: n=%d err=%v", n, err)
	}
}

// A crash leaves the attempt dir behind; the next start must clean it.
func TestSweepScratchRemovesEmptyJobDir(t *testing.T) {
	root := t.TempDir()
	jobDir := filepath.Join(root, "job_x")
	attempt := filepath.Join(jobDir, "a1")
	if err := os.MkdirAll(attempt, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(attempt, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(jobDir, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := SweepScratch(root, time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatal("empty job directory was left behind")
	}
}

func TestSweepScratchSkipsRecentDirs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "job_y", "a1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	n, err := SweepScratch(root, time.Hour, nil)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, want 0", n, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("recent attempt directory removed")
	}
}

// stubEngine returns a fixed document and records how often it ran.
type stubEngine struct {
	name    string
	device  string
	modes   []string
	text    string
	warn    string
	warns   []string
	ocr     *documentir.OCRQuality
	err     error
	calls   int
	parseMS int64
}

func (s *stubEngine) Name() string    { return s.name }
func (s *stubEngine) Version() string { return "stub-1" }
func (s *stubEngine) Device() string {
	if s.device != "" {
		return s.device
	}
	return "cpu"
}
func (s *stubEngine) Formats() []string           { return []string{"pdf", "png"} }
func (s *stubEngine) Modes() []string             { return s.modes }
func (s *stubEngine) Probe(context.Context) error { return nil }

// stubDoc materialises what the stub engine would have returned, so a test can
// hand maybeEscalate the document the fast engine produced.
func stubDoc(s *stubEngine) *documentir.Document {
	d := documentir.New()
	d.Elements = []documentir.Element{{ID: "el_1", Type: documentir.ElParagraph, Text: s.text}}
	if s.warn != "" {
		d.AddWarning(s.warn, "stub", "warn")
	}
	for _, w := range s.warns {
		d.AddWarning(w, "stub", "warn")
	}
	d.OCR = s.ocr
	d.Metrics.ParseMs = s.parseMS
	return d
}

// lowOCR is the evidence a tesseract run on a garbled page reports.
func lowOCR(score float64) *documentir.OCRQuality {
	return &documentir.OCRQuality{Used: true, Scored: true, MinScore: score, MinScorePage: 1}
}

func (s *stubEngine) Parse(_ context.Context, req engines.Request) (*documentir.Document, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return stubDoc(s), nil
}

// PR-B: a *measured* low confidence on CJK text is retried once, and the
// fallback plus its cost are recorded.
func TestEscalationUsesAccurateEngine(t *testing.T) {
	fast := &stubEngine{name: "fast", modes: []string{"fast", "auto"},
		text: "采购订单 金额", ocr: lowOCR(0.26), parseMS: 10}
	accurate := &stubEngine{name: "accurate", modes: []string{"accurate"},
		text: "采购订单 金额 人民币 128,400 元 交付日期", parseMS: 40}
	w := New(Config{WorkDir: t.TempDir(), EscalateScans: true},
		engines.NewRegistry(fast, accurate), slog.New(slog.NewTextHandler(io.Discard, nil)))
	task := &coordClaim{JobID: "job_x", Format: "pdf", Mode: "auto", Device: "auto"}

	doc, used, _ := w.maybeEscalate(context.Background(), task, fast, stubDoc(fast), t.TempDir(), "/tmp/in.pdf")
	if used.Name() != "accurate" {
		t.Fatalf("engine = %s, want accurate", used.Name())
	}
	if !doc.Routing.Fallback {
		t.Fatal("routing.fallback not set")
	}
	for _, want := range []string{"ocr_quality_escalation", "trigger=low_confidence:0.26@1",
		"attempts=2", "decision=accepted", "fast_ms=10", "accurate_ms=40"} {
		if !strings.Contains(doc.Routing.Reason, want) {
			t.Fatalf("routing reason %q missing %q", doc.Routing.Reason, want)
		}
	}
	if doc.Metrics.ParseMs != 50 {
		t.Fatalf("parse time not accumulated: %d", doc.Metrics.ParseMs)
	}
	if accurate.calls != 1 || fast.calls != 0 {
		t.Fatalf("calls fast=%d accurate=%d", fast.calls, accurate.calls)
	}
}

// PR-B: the retry is not accepted when the accurate engine's own evidence is
// worse. Text length is not a quality signal and is not consulted.
func TestEscalationRejectsLowConfidenceRetry(t *testing.T) {
	fast := &stubEngine{name: "fast", modes: []string{"fast", "auto"},
		text: "采购订单 金额", ocr: lowOCR(0.26), parseMS: 30}
	accurate := &stubEngine{name: "accurate", modes: []string{"accurate"},
		text: "采购订单 金额 更多 更多 更多 更多", ocr: lowOCR(0.41), parseMS: 90}
	w := New(Config{WorkDir: t.TempDir(), EscalateScans: true},
		engines.NewRegistry(fast, accurate), slog.New(slog.NewTextHandler(io.Discard, nil)))
	task := &coordClaim{JobID: "job_x", Format: "pdf", Mode: "auto", Device: "auto"}

	doc, used, _ := w.maybeEscalate(context.Background(), task, fast, stubDoc(fast), t.TempDir(), "/tmp/in.pdf")
	if used.Name() != "fast" || doc.Routing.Fallback {
		t.Fatalf("worse retry replaced the fast result: engine=%s fallback=%v", used.Name(), doc.Routing.Fallback)
	}
	if !strings.Contains(doc.Routing.Reason, "decision=rejected:alt_low_confidence(0.41)") {
		t.Fatalf("rejection not recorded: %q", doc.Routing.Reason)
	}
	if doc.Metrics.ParseMs != 120 {
		t.Fatalf("rejected retry time not charged: %d", doc.Metrics.ParseMs)
	}
}

// An empty retry is not an improvement either.
func TestEscalationRejectsEmptyRetry(t *testing.T) {
	fast := &stubEngine{name: "fast", modes: []string{"fast", "auto"}, text: "采购订单 金额", ocr: lowOCR(0.26)}
	accurate := &stubEngine{name: "accurate", modes: []string{"accurate"}, text: ""}
	w := New(Config{WorkDir: t.TempDir(), EscalateScans: true},
		engines.NewRegistry(fast, accurate), slog.New(slog.NewTextHandler(io.Discard, nil)))
	task := &coordClaim{JobID: "job_x", Format: "pdf", Mode: "auto", Device: "auto"}

	doc, used, _ := w.maybeEscalate(context.Background(), task, fast, stubDoc(fast), t.TempDir(), "/tmp/in.pdf")
	if used.Name() != "fast" || !strings.Contains(doc.Routing.Reason, "decision=rejected:empty") {
		t.Fatalf("empty retry accepted: engine=%s reason=%q", used.Name(), doc.Routing.Reason)
	}
}

// PR-B requirement 7: a failed retry keeps the fast result, publishes nothing
// half-written, and still records that the attempt happened.
func TestEscalationFailureKeepsFastResult(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{"engine error", errors.New("boom"), func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}},
		{"timeout", context.DeadlineExceeded, func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			return ctx, cancel
		}},
		{"cancelled", context.Canceled, func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		}},
	} {
		fast := &stubEngine{name: "fast", modes: []string{"fast", "auto"}, text: "采购订单 金额", ocr: lowOCR(0.26), parseMS: 12}
		accurate := &stubEngine{name: "accurate", modes: []string{"accurate"}, err: c.err, parseMS: 0}
		w := New(Config{WorkDir: t.TempDir(), EscalateScans: true},
			engines.NewRegistry(fast, accurate), slog.New(slog.NewTextHandler(io.Discard, nil)))
		task := &coordClaim{JobID: "job_x", Format: "pdf", Mode: "auto", Device: "auto"}
		ctx, cancel := c.ctx()
		defer cancel()

		doc, used, _ := w.maybeEscalate(ctx, task, fast, stubDoc(fast), t.TempDir(), "/tmp/in.pdf")
		if used.Name() != "fast" || doc.Routing.Fallback {
			t.Fatalf("%s: fast result lost: engine=%s fallback=%v", c.name, used.Name(), doc.Routing.Fallback)
		}
		if doc.Elements[0].Text != "采购订单 金额" {
			t.Fatalf("%s: result mutated: %q", c.name, doc.Elements[0].Text)
		}
		if !strings.Contains(doc.Routing.Reason, "decision=rejected:error") {
			t.Fatalf("%s: decision not recorded: %q", c.name, doc.Routing.Reason)
		}
	}
}

// PR-B: "no confidence" is not "low confidence"; a good PP-OCRv6 result must not
// be re-parsed.
func TestNoEscalationOnUnknownQuality(t *testing.T) {
	fast := &stubEngine{name: "fast", modes: []string{"fast", "auto"},
		text:  "采购订单 金额 人民币 128,400 元 交付日期 供应商 华北物流有限公司 复核",
		warns: []string{documentir.WarnOCRUsed, documentir.WarnOCRUsedUnknown},
		ocr:   &documentir.OCRQuality{Used: true}, parseMS: 1300}
	accurate := &stubEngine{name: "accurate", modes: []string{"accurate"}, text: "…"}
	w := New(Config{WorkDir: t.TempDir(), EscalateScans: true},
		engines.NewRegistry(fast, accurate), slog.New(slog.NewTextHandler(io.Discard, nil)))
	task := &coordClaim{JobID: "job_x", Format: "pdf", Mode: "auto", Device: "auto"}

	doc, used, _ := w.maybeEscalate(context.Background(), task, fast, stubDoc(fast), t.TempDir(), "/tmp/in.pdf")
	if used.Name() != "fast" || accurate.calls != 0 {
		t.Fatalf("unknown quality escalated: engine=%s accurate_calls=%d", used.Name(), accurate.calls)
	}
	if doc.Metrics.ParseMs != 1300 {
		t.Fatalf("no retry, but parse time changed: %d", doc.Metrics.ParseMs)
	}
}

func TestEscalationDisabledAndExplicitModes(t *testing.T) {
	fast := &stubEngine{name: "fast", modes: []string{"fast", "auto"}, text: "采购订单 金额", ocr: lowOCR(0.26)}
	accurate := &stubEngine{name: "accurate", modes: []string{"accurate"}, text: "采购订单 金额 更多"}
	reg := engines.NewRegistry(fast, accurate)
	for _, c := range []struct {
		name    string
		enabled bool
		mode    string
	}{
		{"disabled by config", false, "auto"},
		{"explicit fast mode", true, "fast"},
		{"accurate mode", true, "accurate"},
	} {
		w := New(Config{WorkDir: t.TempDir(), EscalateScans: c.enabled}, reg,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		task := &coordClaim{JobID: "job_x", Format: "pdf", Mode: c.mode, Device: "auto"}
		if _, used, _ := w.maybeEscalate(context.Background(), task, fast, stubDoc(fast), t.TempDir(), "/tmp/in.pdf"); used.Name() != "fast" {
			t.Fatalf("%s: escalated unexpectedly to %s", c.name, used.Name())
		}
	}
}

func TestGPUDeviceTypesAreMeasured(t *testing.T) {
	for _, d := range []string{"gpu", "cuda", "mps"} {
		if !isGPUDevice(d) {
			t.Fatalf("%q must be treated as a GPU device", d)
		}
	}
	for _, d := range []string{"", "cpu"} {
		if isGPUDevice(d) {
			t.Fatalf("%q must not be treated as a GPU device", d)
		}
	}
}

// The engine subprocess runs with cwd = the attempt directory, so its arguments must be
// absolute. The default --work-dir is relative, which made a fresh checkout fail every CLI
// parse with "File not found"; New() resolves it once.
func TestWorkerResolvesWorkDir(t *testing.T) {
	w := New(Config{WorkDir: "relative/scratch"}, nil, nil)
	if !filepath.IsAbs(w.Cfg.WorkDir) {
		t.Fatalf("work dir not absolute: %q", w.Cfg.WorkDir)
	}
	if got := filepath.Join(w.Cfg.WorkDir, "job", "a1"); !filepath.IsAbs(got) {
		t.Fatalf("derived attempt dir not absolute: %q", got)
	}
}

// Audit follow-up: a rejected result must end the job, not leave it 'running' until the
// lease expires and re-parse the document on every attempt.
func TestClassifyUploadRejection(t *testing.T) {
	code, msg := classifyUploadRejection([]byte(`{"error":{"code":"malformed_file","message":"table too big"}}`))
	if code != jobs.ErrMalformed || msg != "table too big" {
		t.Fatalf("got %q/%q", code, msg)
	}
	code, _ = classifyUploadRejection([]byte("not json"))
	if code != jobs.ErrInternal {
		t.Fatalf("fallback code = %q", code)
	}
}

// --- PR-D1: gpu_required is a contract, not a label -------------------------

func TestGPURequirementMatrix(t *testing.T) {
	cases := []struct {
		name     string
		device   string
		evidence engines.Execution
		wantErr  bool
	}{
		{"gpu_required + proven GPU", jobs.DeviceGPURequired,
			engines.GPUEvidence("mineru:daemon-log-window", "sessions on CUDA"), false},
		{"gpu_required + known CPU provider", jobs.DeviceGPURequired,
			engines.CPUEvidence("mineru:daemon-log-window", "sessions on CPUExecutionProvider"), true},
		{"gpu_required + unprovable", jobs.DeviceGPURequired,
			engines.UnknownEvidence("docling-serve:no-marker", "no device reported"), true},
		{"gpu_required + adapter said nothing", jobs.DeviceGPURequired, engines.Execution{}, true},
		{"gpu_preferred + CPU fallback is allowed", jobs.DeviceGPUPreferred,
			engines.CPUEvidence("docling-rs:default-cpu", "cpu"), false},
		{"gpu_preferred + unknown is allowed", jobs.DeviceGPUPreferred,
			engines.UnknownEvidence("mineru:no-session-line", "unknown"), false},
		{"cpu request never fails on evidence", jobs.DeviceCPU,
			engines.CPUEvidence("xberg:adapter-cpu-only", "cpu"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := gpuRequirementError(c.device, "engine-x", c.evidence)
			if c.wantErr && err == nil {
				t.Fatalf("gpu_required accepted %v", c.evidence)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
			if err != nil {
				msg := err.Error()
				// The operator must be able to see *why* from the error alone.
				for _, want := range []string{"gpu_required", "engine-x", "cannot prove GPU execution"} {
					if !strings.Contains(msg, want) {
						t.Fatalf("error %q missing %q", msg, want)
					}
				}
			}
		})
	}
}

// actual_device must never claim "gpu" without proof, whatever the engine says.
func TestActualDeviceComesFromEvidence(t *testing.T) {
	for _, c := range []struct {
		evidence engines.Execution
		want     string
	}{
		{engines.GPUEvidence("mineru:daemon-log-window", ""), "gpu"},
		{engines.CPUEvidence("docling-rs:default-cpu", ""), "cpu"},
		{engines.UnknownEvidence("docling-serve:no-marker", ""), "unknown"},
		{engines.Execution{}, "unknown"},
	} {
		if got := actualDevice(c.evidence); got != c.want {
			t.Fatalf("actualDevice(%v) = %q, want %q", c.evidence, got, c.want)
		}
	}
}

// Registration must not present a visible GPU as a verified inference capability.
func TestRegistrationDoesNotClaimVerifiedGPU(t *testing.T) {
	w := &Worker{Cfg: Config{DeviceType: "gpu"}, Reg: engines.NewRegistry(&stubEngine{
		name: "mineru", modes: []string{"accurate"}, device: "gpu",
	})}
	for name, state := range w.gpuEvidenceAtRegistration() {
		if state == "verified" || state == "gpu" {
			t.Fatalf("engine %s claimed verified GPU inference from device visibility", name)
		}
		if state != "unverified" {
			t.Fatalf("gpu worker engine %s = %q, want unverified", name, state)
		}
	}
	cpu := &Worker{Cfg: Config{DeviceType: "cpu"}, Reg: engines.NewRegistry(&stubEngine{
		name: "xberg", modes: []string{"fast", "auto"}, device: "cpu",
	})}
	for name, state := range cpu.gpuEvidenceAtRegistration() {
		if state != "cpu_only" {
			t.Fatalf("cpu worker engine %s = %q, want cpu_only", name, state)
		}
	}
}

package worker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	modes   []string
	text    string
	warn    string
	calls   int
	parseMS int64
}

func (s *stubEngine) Name() string                { return s.name }
func (s *stubEngine) Version() string             { return "stub-1" }
func (s *stubEngine) Device() string              { return "cpu" }
func (s *stubEngine) Formats() []string           { return []string{"pdf", "png"} }
func (s *stubEngine) Modes() []string             { return s.modes }
func (s *stubEngine) Probe(context.Context) error { return nil }
func (s *stubEngine) Parse(_ context.Context, req engines.Request) (*documentir.Document, error) {
	s.calls++
	d := documentir.New()
	d.Elements = []documentir.Element{{ID: "el_1", Type: documentir.ElParagraph, Text: s.text}}
	if s.warn != "" {
		d.AddWarning(s.warn, "stub", "warn")
	}
	d.Metrics.ParseMs = s.parseMS
	return d, nil
}

// RC-3 / PR-04: a poor OCR result on CJK text is retried once with the accurate
// engine, and the fallback is recorded in the IR.
func TestEscalationUsesAccurateEngine(t *testing.T) {
	fast := &stubEngine{name: "fast", modes: []string{"fast", "auto"},
		text: "采购订单 金额", warn: documentir.WarnOCRLowConfidence, parseMS: 10}
	accurate := &stubEngine{name: "accurate", modes: []string{"accurate"},
		text: "采购订单 金额 人民币 128,400 元 交付日期", parseMS: 40}
	w := New(Config{WorkDir: t.TempDir(), EscalateScans: true},
		engines.NewRegistry(fast, accurate), slog.New(slog.NewTextHandler(io.Discard, nil)))
	task := &coordClaim{JobID: "job_x", Format: "pdf", Mode: "auto", Device: "auto"}

	doc, used := w.maybeEscalate(context.Background(), task, fast, stubDoc(fast), t.TempDir(), "/tmp/in.pdf")
	if used.Name() != "accurate" {
		t.Fatalf("engine = %s, want accurate", used.Name())
	}
	if !doc.Routing.Fallback {
		t.Fatal("routing.fallback not set")
	}
	if !strings.Contains(doc.Routing.Reason, "ocr_quality_escalation") {
		t.Fatalf("routing reason not auditable: %q", doc.Routing.Reason)
	}
	if doc.Metrics.ParseMs != 50 {
		t.Fatalf("parse time not accumulated: %d", doc.Metrics.ParseMs)
	}
	if accurate.calls != 1 || fast.calls != 0 {
		t.Fatalf("calls fast=%d accurate=%d", fast.calls, accurate.calls)
	}
}

// A retry that produces less text is rejected: escalation must not degrade output.
func TestEscalationKeepsFastResultWhenAccurateIsWorse(t *testing.T) {
	fast := &stubEngine{name: "fast", modes: []string{"fast", "auto"},
		text: "采购订单 金额 人民币 128,400 元 交付日期 供应商 华北物流", warn: documentir.WarnOCRLowConfidence}
	accurate := &stubEngine{name: "accurate", modes: []string{"accurate"}, text: "采购"}
	w := New(Config{WorkDir: t.TempDir(), EscalateScans: true},
		engines.NewRegistry(fast, accurate), slog.New(slog.NewTextHandler(io.Discard, nil)))
	task := &coordClaim{JobID: "job_x", Format: "pdf", Mode: "auto", Device: "auto"}

	doc, used := w.maybeEscalate(context.Background(), task, fast, stubDoc(fast), t.TempDir(), "/tmp/in.pdf")
	if used.Name() != "fast" || doc.Routing.Fallback {
		t.Fatalf("worse result replaced the fast one: engine=%s fallback=%v", used.Name(), doc.Routing.Fallback)
	}
}

func TestEscalationDisabledAndExplicitModes(t *testing.T) {
	fast := &stubEngine{name: "fast", modes: []string{"fast", "auto"},
		text: "采购订单 金额", warn: documentir.WarnOCRLowConfidence}
	accurate := &stubEngine{name: "accurate", modes: []string{"accurate"}, text: "采购订单 金额 更多"}
	reg := engines.NewRegistry(fast, accurate)
	for _, c := range []struct {
		name    string
		enabled bool
		mode    string
	}{
		{"disabled by config", false, "auto"},
		{"explicit fast mode", true, "fast"},
	} {
		w := New(Config{WorkDir: t.TempDir(), EscalateScans: c.enabled}, reg,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		task := &coordClaim{JobID: "job_x", Format: "pdf", Mode: c.mode, Device: "auto"}
		if _, used := w.maybeEscalate(context.Background(), task, fast, stubDoc(fast), t.TempDir(), "/tmp/in.pdf"); used.Name() != "fast" {
			t.Fatalf("%s: escalated unexpectedly to %s", c.name, used.Name())
		}
	}
}

func stubDoc(s *stubEngine) *documentir.Document {
	d := documentir.New()
	d.Elements = []documentir.Element{{ID: "el_1", Type: documentir.ElParagraph, Text: s.text}}
	if s.warn != "" {
		d.AddWarning(s.warn, "stub", "warn")
	}
	d.Metrics.ParseMs = s.parseMS
	return d
}

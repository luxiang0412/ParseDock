package engines

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionDeviceMapping(t *testing.T) {
	cases := []struct {
		exec Execution
		want string
	}{
		{GPUEvidence("x", "y"), "gpu"},
		{CPUEvidence("x", "y"), "cpu"},
		{UnknownEvidence("x", "y"), "unknown"},
		{Execution{}, "unknown"},
	}
	for _, c := range cases {
		if got := c.exec.Device(); got != c.want {
			t.Fatalf("%v.Device() = %q, want %q", c.exec, got, c.want)
		}
	}
	if !GPUEvidence("x", "y").VerifiedGPU() {
		t.Fatal("gpu evidence must be verified")
	}
	for _, e := range []Execution{CPUEvidence("x", "y"), UnknownEvidence("x", "y"), {}} {
		if e.VerifiedGPU() {
			t.Fatalf("%v must not count as verified GPU", e)
		}
	}
	if got := GPUEvidence("daemon-log:window", "").String(); got != "gpu_verified(daemon-log:window)" {
		t.Fatalf("String() = %q", got)
	}
	if got := (Execution{}).String(); got != "unknown(none)" {
		t.Fatalf("zero String() = %q", got)
	}
}

// docling-rs: only two outcomes are provable - no provider requested, or a
// requested provider that reported a CPU fallback.
func TestDoclingExecution(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		stderr string
		want   ExecutionKind
	}{
		{"no EP requested is CPU", map[string]string{}, "", ExecutionCPUVerified},
		{"explicit cpu EP is CPU", map[string]string{"DOCLING_RS_EP": "cpu"}, "", ExecutionCPUVerified},
		{"cuda requested, fallback reported", map[string]string{"DOCLING_RS_EP": "cuda"},
			"docling-rs: DOCLING_RS_EP=\"cuda\" requested, but this binary was built without that provider - rebuild with `--features cuda`; using CPU",
			ExecutionCPUVerified},
		{"cuda requested, nothing reported stays unknown", map[string]string{"DOCLING_RS_EP": "cuda"}, "",
			ExecutionUnknown},
		{"tensorrt requested, no marker stays unknown", map[string]string{"DOCLING_RS_EP": "tensorrt"}, "",
			ExecutionUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := doclingExecution([]byte(c.stderr), c.env)
			if got.Kind != c.want {
				t.Fatalf("kind = %q (%s), want %q", got.Kind, got.Detail, c.want)
			}
			if got.Source == "" || got.Detail == "" {
				t.Fatalf("evidence without provenance: %+v", got)
			}
		})
	}
}

// xberg is a CPU-only adapter; that is a statement about the adapter, and it is
// overridden by the engine's own message if the build ever changes.
func TestXbergExecution(t *testing.T) {
	if got := xbergExecution(nil); got.Kind != ExecutionCPUVerified || got.Source != "xberg:adapter-cpu-only" {
		t.Fatalf("unexpected evidence: %+v", got)
	}
	got := xbergExecution([]byte("Ort error: CUDA execution provider requested but this build was compiled without CUDA support; rebuild with the `cuda` feature."))
	if got.Kind != ExecutionCPUVerified || got.Source != "xberg:stderr-no-cuda" {
		t.Fatalf("stderr marker not honoured: %+v", got)
	}
}

// MinerU: only lines written during this task count. A CUDA provider mentioned
// at daemon start-up is exactly the false positive to avoid.
func TestMineruExecutionUsesOnlyTheTaskWindow(t *testing.T) {
	cpuLine := `2026-10-31 10:52:47,828 [INFO] mineru.model.runtime.onnx: ONNX session ready: kind=ocr_det, providers=['CPUExecutionProvider'], gpu=None, init_s=0.040`
	gpuLine := `2026-10-31 10:52:47,828 [INFO] mineru.model.runtime.onnx: ONNX session ready: kind=ocr_det, providers=['CUDAExecutionProvider'], gpu=0, init_s=0.040`

	cases := []struct {
		name    string
		lines   []string
		have    bool
		want    ExecutionKind
		wantSrc string
	}{
		{"cpu provider during the task", []string{cpuLine}, true, ExecutionCPUVerified, "mineru:daemon-log-window"},
		{"gpu provider during the task", []string{gpuLine}, true, ExecutionGPUVerified, "mineru:daemon-log-window"},
		{"no session line in the window", []string{"2026-10-31 10:52:47,828 [INFO] uvicorn.access: GET /health 200"}, true, ExecutionUnknown, "mineru:no-session-line"},
		{"empty window", nil, true, ExecutionUnknown, "mineru:no-session-line"},
		{"log unreadable", []string{gpuLine}, false, ExecutionUnknown, "mineru:no-log-window"},
		{"mixed providers is not a GPU run", []string{gpuLine, cpuLine}, true, ExecutionCPUVerified, "mineru:daemon-log-window"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mineruExecution(c.lines, c.have, "test detail")
			if got.Kind != c.want || got.Source != c.wantSrc {
				t.Fatalf("evidence = %+v, want kind=%s source=%s", got, c.want, c.wantSrc)
			}
		})
	}
}

// The window must ignore lines that were already in the file before the task.
func TestDaemonLogWindowIgnoresEarlierLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doclib.log")
	gpuStartup := "ONNX session ready: kind=layout, providers=['CUDAExecutionProvider'], gpu=0\n"
	if err := os.WriteFile(path, []byte(gpuStartup), 0o600); err != nil {
		t.Fatal(err)
	}
	window := openDaemonWindow(path)
	if !window.valid {
		t.Fatal("window not established on an existing file")
	}
	lines, ok := window.newLines()
	if !ok {
		t.Fatal("window unreadable")
	}
	if len(lines) != 0 {
		t.Fatalf("start-up lines leaked into the task window: %v", lines)
	}
	if ev := mineruExecution(lines, ok, path); ev.Kind != ExecutionUnknown {
		t.Fatalf("start-up CUDA line was treated as this task's evidence: %+v", ev)
	}

	// Lines appended during the task are evidence.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("ONNX session ready: kind=ocr_det, providers=['CPUExecutionProvider'], gpu=None\n"); err != nil {
		t.Fatal(err)
	}
	lines, ok = window.newLines()
	if !ok || len(lines) != 1 || !strings.Contains(lines[0], "CPUExecutionProvider") {
		t.Fatalf("task window = %v (ok=%v)", lines, ok)
	}
	if ev := mineruExecution(lines, ok, path); ev.Kind != ExecutionCPUVerified {
		t.Fatalf("cpu session not detected: %+v", ev)
	}
}

func TestDaemonLogWindowMissingFileIsUnknown(t *testing.T) {
	window := openDaemonWindow(filepath.Join(t.TempDir(), "nope.log"))
	if _, ok := window.newLines(); ok {
		t.Fatal("missing log must not produce a window")
	}
	if ev := mineruExecution(nil, false, "missing"); ev.Kind != ExecutionUnknown {
		t.Fatalf("missing log must be unknown, got %+v", ev)
	}
}

// An adapter that reports nothing leaves the decision to the caller, which must
// treat it as unknown rather than assuming the declared device.
func TestRequestReportIsOptional(t *testing.T) {
	var got []Execution
	req := Request{ReportExecution: func(e Execution) { got = append(got, e) }}
	req.Report(CPUEvidence("native:in-process", "d"))
	if len(got) != 1 || got[0].Device() != "cpu" {
		t.Fatalf("report not delivered: %+v", got)
	}
	// A nil callback must not panic.
	Request{}.Report(UnknownEvidence("x", "y"))
}

// Start-up evidence may rule a GPU out but must never rule one in.
func TestMineruStartupEvidenceOnlyProvesCPU(t *testing.T) {
	cpu := `ONNX session ready: kind=ocr_det, providers=['CPUExecutionProvider'], gpu=None`
	gpu := `ONNX session ready: kind=ocr_det, providers=['CUDAExecutionProvider'], gpu=0`

	if ev := mineruStartupExecution([]string{cpu, cpu}); ev.Kind != ExecutionCPUVerified ||
		ev.Source != "mineru:daemon-log-startup" {
		t.Fatalf("all-CPU daemon not recognised: %+v", ev)
	}
	if ev := mineruStartupExecution([]string{gpu}); ev.Kind != "" {
		t.Fatalf("a start-up GPU line must not produce evidence, got %+v", ev)
	}
	if ev := mineruStartupExecution([]string{gpu, cpu}); ev.Kind != "" {
		t.Fatalf("mixed start-up history must produce no evidence, got %+v", ev)
	}
	if ev := mineruStartupExecution(nil); ev.Kind != "" {
		t.Fatalf("empty log must produce no evidence, got %+v", ev)
	}
}

// A daemon with CPU-only history keeps gpu_required failing, and gpu_preferred
// records "cpu" instead of "unknown".
func TestStartupEvidenceFillsOnlyTheUnknownGap(t *testing.T) {
	cpuStartup := `ONNX session ready: kind=layout, providers=['CPUExecutionProvider'], gpu=None`
	path := filepath.Join(t.TempDir(), "doclib.log")
	if err := os.WriteFile(path, []byte(cpuStartup+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := openDaemonWindow(path)
	taskLines, ok := w.newLines()
	if ev := mineruExecution(taskLines, ok, path); ev.Kind != ExecutionUnknown {
		t.Fatalf("expected unknown during the task, got %+v", ev)
	}
	startup, ok := w.tailLines(64 << 10)
	if !ok {
		t.Fatal("tail unreadable")
	}
	if ev := mineruStartupExecution(startup); ev.Device() != "cpu" {
		t.Fatalf("startup evidence should resolve to cpu, got %+v", ev)
	}
}

package engines

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// ExecutionKind is how strongly an adapter can prove where one parse ran. It is
// about *this task*, never about the host, the driver, or a daemon's start-up.
type ExecutionKind string

const (
	// ExecutionGPUVerified means the adapter observed a marker emitted while
	// this task ran that identifies a GPU execution provider.
	ExecutionGPUVerified ExecutionKind = "gpu_verified"
	// ExecutionCPUVerified means the adapter can rule a GPU out for this task
	// (CPU-only adapter, engine default, or an observed fallback message).
	ExecutionCPUVerified ExecutionKind = "cpu_verified"
	// ExecutionUnknown means no per-task evidence exists either way.
	ExecutionUnknown ExecutionKind = "unknown"
)

// Execution is the outcome of the evidence search for one parse.
type Execution struct {
	Kind   ExecutionKind
	Source string // short origin: "adapter:cpu-only", "daemon-log:window", ...
	Detail string // one sentence, safe to log and to show in an error
}

// Device is the IR value for this evidence ("gpu", "cpu" or "unknown"). It is
// deliberately not the engine's static claim: an engine that says "gpu" but
// cannot prove it must not be reported as a GPU result.
func (e Execution) Device() string {
	switch e.Kind {
	case ExecutionGPUVerified:
		return "gpu"
	case ExecutionCPUVerified:
		return "cpu"
	default:
		return "unknown"
	}
}

// VerifiedGPU reports whether a gpu_required task may be published.
func (e Execution) VerifiedGPU() bool { return e.Kind == ExecutionGPUVerified }

// String renders the evidence for logs and routing reasons.
func (e Execution) String() string {
	kind := string(e.Kind)
	if kind == "" {
		kind = string(ExecutionUnknown)
	}
	src := e.Source
	if src == "" {
		src = "none"
	}
	return fmt.Sprintf("%s(%s)", kind, src)
}

// CPUEvidence records proof that this task ran on the CPU.
func CPUEvidence(source, detail string) Execution {
	return Execution{Kind: ExecutionCPUVerified, Source: source, Detail: detail}
}

// GPUEvidence records proof that this task ran on a GPU.
func GPUEvidence(source, detail string) Execution {
	return Execution{Kind: ExecutionGPUVerified, Source: source, Detail: detail}
}

// UnknownEvidence records that nothing could be proven about this task.
func UnknownEvidence(source, detail string) Execution {
	return Execution{Kind: ExecutionUnknown, Source: source, Detail: detail}
}

// doclingFallbackMarkers are the stderr lines docling-rs prints when a GPU
// execution provider was requested but the build/host cannot provide it. The
// engine then continues on the CPU with exit code 0, which is exactly the case
// that must not be reported as a GPU run.
var doclingFallbackMarkers = []string{
	"built without that provider",
	"using CPU",
}

// doclingExecution derives the evidence for one docling-rs parse.
//
// The execution provider is selected by the environment (DOCLING_RS_EP), so the
// adapter reads it rather than a flag. Two outcomes are provable:
//
//   - no provider requested (the upstream default): the parse is CPU-only;
//   - a provider was requested and the engine said it fell back: CPU.
//
// A provider request with no fallback marker is *not* proof of GPU inference
// (the engine may print nothing at all), so it stays unknown.
func doclingExecution(stderr []byte, env map[string]string) Execution {
	ep := strings.ToLower(strings.TrimSpace(env["DOCLING_RS_EP"]))
	switch ep {
	case "", "cpu":
		return CPUEvidence("docling-rs:default-cpu",
			"no DOCLING_RS_EP requested; docling-rs runs its CPU execution provider")
	}
	text := strings.ToLower(string(stderr))
	for _, marker := range doclingFallbackMarkers {
		if strings.Contains(text, strings.ToLower(marker)) {
			return CPUEvidence("docling-rs:stderr-fallback",
				fmt.Sprintf("DOCLING_RS_EP=%s was requested but the engine reported a CPU fallback", ep))
		}
	}
	return UnknownEvidence("docling-rs:no-positive-marker",
		fmt.Sprintf("DOCLING_RS_EP=%s was requested; the engine printed no device marker, so GPU inference cannot be proven", ep))
}

// xbergExecution derives the evidence for one xberg parse. The adapter never
// passes --acceleration (and the pinned release build is compiled without CUDA),
// so every xberg parse is a CPU parse. If the engine ever reports otherwise the
// statement is corrected by the stderr marker check below.
func xbergExecution(stderr []byte) Execution {
	text := strings.ToLower(string(stderr))
	if strings.Contains(text, "compiled without cuda support") {
		return CPUEvidence("xberg:stderr-no-cuda",
			"xberg reported that its build has no CUDA support")
	}
	return CPUEvidence("xberg:adapter-cpu-only",
		"the adapter never requests an ONNX accelerator; the pinned xberg build is CPU-only")
}

// daemonLogWindow collects the lines an engine daemon appended while one task
// ran. Reading from a saved offset is what keeps "the daemon once initialised a
// CUDA provider at start-up" apart from "this task used the GPU": start-up lines
// are below the offset and are ignored.
type daemonLogWindow struct {
	path   string
	offset int64
	valid  bool
}

// openDaemonWindow snapshots the current end of path. A missing file yields an
// invalid window, which produces unknown evidence - never a guess.
func openDaemonWindow(path string) daemonLogWindow {
	if strings.TrimSpace(path) == "" {
		return daemonLogWindow{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return daemonLogWindow{path: path}
	}
	return daemonLogWindow{path: path, offset: fi.Size(), valid: true}
}

// newLines returns everything appended since the snapshot.
func (w daemonLogWindow) newLines() ([]string, bool) {
	if !w.valid {
		return nil, false
	}
	f, err := os.Open(w.path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if _, err := f.Seek(w.offset, 0); err != nil {
		return nil, false
	}
	data, err := readAllLimited(f, 4<<20)
	if err != nil {
		return nil, false
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, true
}

// tailLines returns the last limit bytes of the log, split into lines. It exists
// so start-up evidence can be used for one purpose only: proving that every ONNX
// session this daemon ever created is a CPU session. It is never used to claim a
// GPU for the current task - that needs a line written during the task.
func (w daemonLogWindow) tailLines(limit int64) ([]string, bool) {
	if strings.TrimSpace(w.path) == "" {
		return nil, false
	}
	f, err := os.Open(w.path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false
	}
	start := int64(0)
	if fi.Size() > limit {
		start = fi.Size() - limit
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, false
	}
	data, err := readAllLimited(f, limit+1)
	if err != nil {
		return nil, false
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, true
}

// mineruProviderMarkers is what MinerU's daemon writes when it initialises an
// ONNX session: "ONNX session ready: kind=ocr_det,
// providers=['CUDAExecutionProvider'], gpu=0, ...".
const mineruProviderMarker = "providers=["

// mineruExecution derives the evidence for one MinerU parse from the lines its
// daemon wrote while the task ran. Only lines inside the window count: a
// CUDAExecutionProvider mentioned at daemon start-up says nothing about this
// task, and is exactly the false positive this function exists to avoid.
func mineruExecution(lines []string, haveWindow bool, windowDetail string) Execution {
	if !haveWindow {
		return UnknownEvidence("mineru:no-log-window",
			"could not read MinerU's daemon log, so the execution device of this task is unknown: "+windowDetail)
	}
	var providers []string
	for _, line := range lines {
		if !strings.Contains(line, mineruProviderMarker) {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "cudaexecutionprovider"),
			strings.Contains(lower, "gpu=0"),
			strings.Contains(lower, "tensorrtexecutionprovider"):
			providers = append(providers, "gpu")
		case strings.Contains(lower, "cpuexecutionprovider"),
			strings.Contains(lower, "gpu=none"):
			providers = append(providers, "cpu")
		}
	}
	if len(providers) == 0 {
		return UnknownEvidence("mineru:no-session-line",
			"the daemon initialised no ONNX session during this task (models already loaded), so the device of this task is unknown")
	}
	for _, p := range providers {
		if p == "cpu" {
			return CPUEvidence("mineru:daemon-log-window",
				"MinerU's daemon initialised this task's ONNX sessions on CPUExecutionProvider")
		}
	}
	return GPUEvidence("mineru:daemon-log-window",
		fmt.Sprintf("MinerU's daemon initialised %d ONNX session(s) on a GPU execution provider during this task", len(providers)))
}

// readAllLimited reads at most limit bytes from r.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("log window exceeds %d MiB", limit>>20)
	}
	return data, nil
}

// mineruStartupExecution uses the daemon's whole log for the one conclusion it can
// support: if every ONNX session the daemon created was a CPU session, then a
// task served by that daemon cannot be running on a GPU provider. The converse
// is deliberately not implemented - a start-up CUDA line proves nothing about
// this task, and treating it as proof is the exact false positive PR-D1 fixes.
func mineruStartupExecution(lines []string) Execution {
	sessions, gpuSessions := 0, 0
	for _, line := range lines {
		if !strings.Contains(line, mineruProviderMarker) {
			continue
		}
		sessions++
		lower := strings.ToLower(line)
		if strings.Contains(lower, "cudaexecutionprovider") ||
			strings.Contains(lower, "tensorrtexecutionprovider") ||
			strings.Contains(lower, "gpu=0") {
			gpuSessions++
		}
	}
	if sessions == 0 || gpuSessions > 0 {
		return Execution{}
	}
	return CPUEvidence("mineru:daemon-log-startup",
		fmt.Sprintf("all %d ONNX session(s) this daemon initialised use the CPU execution provider, so this task could not have run on a GPU", sessions))
}

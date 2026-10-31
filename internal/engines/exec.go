package engines

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
)

// CLISpec describes how to launch a parser binary.
type CLISpec struct {
	Bin       string
	Env       []string // extra environment (PATH, LD_LIBRARY_PATH, model dirs)
	Sandbox   string   // "", "unshare-net"
	MaxOutput int64    // stdout byte ceiling (0 = unlimited)
	// MaxRSSMB kills the parser when its resident set exceeds this ceiling
	// (0 = unlimited). Cgroup limits are the deployment-level mechanism; this is
	// the per-invocation guard so one oversized document cannot take the worker
	// down with it.
	MaxRSSMB int64
}

// ErrOOM reports that a parser was killed for exceeding its memory ceiling.
var ErrOOM = errors.New("engine exceeded its memory ceiling")

// ExecResult is the measured outcome of one subprocess parse.
type ExecResult struct {
	Stdout    []byte
	Stderr    []byte
	Elapsed   time.Duration
	PeakRSSMB int64
	ExitCode  int
	TimedOut  bool
	// OOM is set when the RSS monitor killed the parser; ExitCode is forced
	// non-zero so callers cannot mistake the kill for a clean exit.
	OOM bool
	// Truncated is set when stdout exceeded the configured ceiling and was cut.
	Truncated bool
}

// ErrOutputTooLarge reports that a parser produced more output than the ceiling allows.
var ErrOutputTooLarge = errors.New("engine output exceeded its size ceiling")

// RunCLI executes a parser binary with a hard timeout and optional network
// namespace isolation.
//
// Isolation options (documented in docs/DEPLOYMENT.md as defense in depth):
//   - "unshare-net": run under `unshare -n` so the parser cannot reach the
//     network at all. Requires CAP_SYS_ADMIN or unprivileged user namespaces.
//   - resource limits are applied by the container in production; on bare
//     metal the timeout plus the worker's own concurrency cap bound the damage.
func RunCLI(ctx context.Context, spec CLISpec, workDir string, args ...string) (*ExecResult, error) {
	if spec.Bin == "" {
		return nil, errors.New("engine binary not configured")
	}
	bin := spec.Bin
	// A relative binary path is resolved by the kernel against Cmd.Dir below, not against the
	// directory the coordinator was started in, so "--xberg-bin runtime/engines/..." would fail
	// with "no such file" inside every attempt directory. Pin it to the operator's cwd.
	if strings.ContainsRune(bin, os.PathSeparator) && !filepath.IsAbs(bin) {
		if abs, err := filepath.Abs(bin); err == nil {
			bin = abs
		}
	}
	if spec.Sandbox == "unshare-net" {
		if err := checkUnshare(ctx); err != nil {
			// Fail closed: never silently run an engine without the isolation
			// the operator asked for.
			return nil, fmt.Errorf("%w: network sandbox unavailable: %v", ErrSandboxUnavailable, err)
		}
		bin = "unshare"
		args = append([]string{"-n", "--", spec.Bin}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// Note: workDir and args must be absolute. cmd.Dir sets the child's cwd, so a relative
	// arg would be resolved against workDir and fail; worker.New resolves its scratch root so
	// every derived path is absolute.
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "TERM=dumb")
	cmd.Env = append(cmd.Env, spec.Env...)
	var stdout, stderr bytes.Buffer
	if spec.MaxOutput > 0 {
		cmd.Stdout = &limitedWriter{w: &stdout, n: spec.MaxOutput}
	} else {
		cmd.Stdout = &stdout
	}
	cmd.Stderr = &limitedWriter{w: &stderr, n: 1 << 20}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole process group so helper processes die too.
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stopMonitor := make(chan struct{})
	oom := make(chan struct{})
	monitorDone := make(chan struct{})
	if spec.MaxRSSMB > 0 {
		go func(pid int) {
			// monitorDone is closed after the outcome channel has been published,
			// so Wait() returning first cannot make us miss an OOM kill.
			defer close(monitorDone)
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stopMonitor:
					return
				case <-t.C:
					// Engines spawn helpers (tesseract, multiprocessing workers);
					// measure the whole process group, not just the launcher.
					if rss, ok := processGroupRSSMB(pid); ok && rss > spec.MaxRSSMB {
						_ = syscall.Kill(-pid, syscall.SIGKILL)
						close(oom)
						return
					}
				}
			}
		}(cmd.Process.Pid)
	}
	err := cmd.Wait()
	close(stopMonitor)
	if spec.MaxRSSMB > 0 {
		<-monitorDone // join the monitor before classifying the result
	}
	elapsed := time.Since(start)
	res := &ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Elapsed: elapsed}
	if lw, ok := cmd.Stdout.(*limitedWriter); ok && lw.overflow {
		// The parser produced more than we are willing to hold: say so explicitly instead of
		// letting a truncated JSON document look like a parse failure.
		res.Truncated = true
	}
	// Fill the measured status *before* any early return: callers inspect
	// ExitCode and PeakRSSMB to classify failures, and a memory kill must not look
	// like a clean exit with output.
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok && ru != nil {
			// Linux reports ru_maxrss in kibibytes.
			res.PeakRSSMB = ru.Maxrss / 1024
		}
	}
	select {
	case <-oom:
		res.OOM = true
		if res.ExitCode == 0 {
			// Killed by the monitor; the exit code is not meaningful, but zero
			// would be read as success by the adapters.
			res.ExitCode = -1
		}
		return res, ErrOOM
	default:
	}
	if ctx.Err() != nil {
		res.TimedOut = true
		return res, context.DeadlineExceeded
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

// processGroupRSSMB sums the resident set of every process in the group led by
// pgid (we start every parser with Setpgid, so the group id equals its pid).
func processGroupRSSMB(pgid int) (int64, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	var total int64
	found := false
	for _, e := range entries {
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil {
			continue
		}
		if groupOf(pid) != pgid {
			continue
		}
		if rss, ok := processRSSMB(pid); ok {
			total += rss
			found = true
		}
	}
	return total, found
}

// groupOf returns a process's process-group id, or 0 when it cannot be read.
func groupOf(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// The comm field is parenthesised and may contain spaces; parse after it.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 > len(b) {
		return 0
	}
	fields := bytes.Fields(b[i+2:])
	if len(fields) < 3 {
		return 0
	}
	// state(0) ppid(1) pgrp(2)
	pgid, perr := strconv.Atoi(string(fields[2]))
	if perr != nil {
		return 0
	}
	return pgid
}

// processRSSMB reports the current resident set of a process in MiB.
func processRSSMB(pid int) (int64, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("VmRSS:")) {
			fields := bytes.Fields(line)
			if len(fields) < 2 {
				return 0, false
			}
			kb, perr := strconv.ParseInt(string(fields[1]), 10, 64)
			if perr != nil {
				return 0, false
			}
			return kb / 1024, true
		}
	}
	return 0, false
}

type limitedWriter struct {
	w *bytes.Buffer
	n int64
	// overflow records that output was truncated, so callers can reject the result
	// deliberately instead of treating it as a parser failure.
	overflow bool
}

// Write keeps draining past the limit while retaining only the first l.n bytes, and always
// reports the full length it consumed. Returning a short count with a nil error breaks the
// io.Writer contract (io.Copy answers io.ErrShortWrite), which stopped the drain and let the
// child block on a full pipe until the timeout fired.
func (l *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if l.n <= 0 {
		l.overflow = true
		return n, nil
	}
	keep := p
	if int64(len(keep)) > l.n {
		keep = keep[:l.n]
		l.overflow = true
	}
	w, err := l.w.Write(keep)
	l.n -= int64(w)
	if err != nil {
		return w, err
	}
	return n, nil
}

// ErrSandboxUnavailable reports that a requested isolation primitive is not
// usable in this environment (no CAP_SYS_ADMIN, no unprivileged user namespaces).
var ErrSandboxUnavailable = errors.New("sandbox unavailable")

var unshareOnce sync.Once
var unshareErr error

// checkUnshare probes unprivileged network-namespace isolation once per process.
func checkUnshare(ctx context.Context) error {
	unshareOnce.Do(func() {
		probe, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(probe, "unshare", "-n", "true")
		if out, err := cmd.CombinedOutput(); err != nil {
			unshareErr = fmt.Errorf("%v (%s)", err, strings.TrimSpace(string(out)))
		}
	})
	if unshareErr != nil {
		return unshareErr
	}
	return ctx.Err()
}

// workFile returns a scratch path inside the worker's per-attempt directory.
func workFile(workDir, name string) string { return filepath.Join(workDir, name) }

// ClassifyCLIError turns a parser's stderr into a stable error code plus a short,
// useful message. Log noise (WARN/INFO lines, timestamps) is dropped: the previous
// "tail 400 bytes" approach truncated into the middle of a warning and hid the
// actual cause in a real-corpus run (PR-01 follow-up).
func ClassifyCLIError(stderr string) (code string, message string) {
	lines := strings.Split(stderr, "\n")
	keep := make([]string, 0, len(lines))
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		up := strings.ToUpper(l)
		if strings.Contains(up, " WARN ") || strings.Contains(up, " INFO ") ||
			strings.Contains(up, " DEBUG ") || strings.Contains(up, " TRACE ") {
			continue
		}
		keep = append(keep, l)
	}
	msg := strings.Join(keep, " | ")
	if msg == "" {
		msg = strings.TrimSpace(stderr)
	}
	if len(msg) > 600 {
		msg = msg[len(msg)-600:]
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "archive too large"),
		strings.Contains(low, "exceeds docling_rs_sheet_max_cells"),
		strings.Contains(low, "document_too_large"):
		return jobs.ErrExpandedTooLarge, "document expands beyond the parser limits: " + msg
	case strings.Contains(low, "timed out"), strings.Contains(low, "deadline exceeded"):
		return jobs.ErrTimeout, msg
	case strings.Contains(low, "password"), strings.Contains(low, "encrypted"):
		return jobs.ErrEncrypted, msg
	case strings.Contains(low, "unsupported"), strings.Contains(low, "not supported"):
		return jobs.ErrUnsupportedType, msg
	}
	return jobs.ErrMalformed, msg
}

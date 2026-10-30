package engines

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
}

// ExecResult is the measured outcome of one subprocess parse.
type ExecResult struct {
	Stdout    []byte
	Stderr    []byte
	Elapsed   time.Duration
	PeakRSSMB int64
	ExitCode  int
	TimedOut  bool
}

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
	err := cmd.Run()
	elapsed := time.Since(start)
	res := &ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Elapsed: elapsed}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok && ru != nil {
			// Linux reports ru_maxrss in kibibytes.
			res.PeakRSSMB = ru.Maxrss / 1024
		}
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

type limitedWriter struct {
	w *bytes.Buffer
	n int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.w.Write(p)
	l.n -= int64(n)
	return len(p), err
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

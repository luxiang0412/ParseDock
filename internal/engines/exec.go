package engines

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

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
	// KilledBySignal is set when the parser died from a SIGKILL that RunCLI did not send
	// (neither the timeout nor the RSS monitor). On Linux that is almost always the kernel
	// or cgroup OOM killer, which leaves no stderr behind.
	KilledBySignal bool
}

// cliWaitDelay bounds how long RunCLI waits for stdout/stderr to close after the parser
// itself has exited (or been killed). A grandchild that inherited the pipes would
// otherwise hold Wait open for as long as it lives, far past the parse timeout. It is a
// variable so tests can shorten it.
var cliWaitDelay = 2 * time.Second

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
		// Pass the resolved path: unshare runs with the same cwd (the attempt directory),
		// where the operator's relative spec.Bin does not exist.
		args = append([]string{"-n", "--", bin}, args...)
		bin = "unshare"
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
	// ctxKilled records that *we* killed the parser because ctx ended. exec calls Cancel
	// only when ctx is done before the process exits, so this separates "timed out" from
	// "finished, then Wait sat on a pipe held by a grandchild until the deadline passed".
	var ctxKilled atomic.Bool
	cmd.Cancel = func() error {
		// Kill the whole process group so helper processes die too.
		if cmd.Process == nil {
			return nil
		}
		ctxKilled.Store(true)
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = cliWaitDelay

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
	// Reap whatever is left of the process group (helpers, a grandchild that held the
	// pipes). The group id is the leader's pid; ESRCH just means nothing was left.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		// The parser exited cleanly and only a leftover child kept a pipe open; what the
		// parser wrote is complete, so this is not a failure.
		err = nil
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
	if ctxKilled.Load() {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.TimedOut = true
			return res, context.DeadlineExceeded
		}
		// Cancelled by the caller (job cancelled, lease lost, worker shutting down): not a
		// timeout, and not a property of the document.
		return res, context.Canceled
	}
	if cmd.ProcessState != nil {
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL {
			// Nothing in this process sent the kill, so it came from outside: the kernel or
			// the container's cgroup OOM killer in practice.
			res.KilledBySignal = true
		}
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

// cliFailure maps a failed RunCLI outcome to a stable engine error. It is the single
// place the CLI adapters (xberg, docling-rs, mineru) classify a failed run, so a rule
// such as "an external SIGKILL is an OOM kill" changes in one spot.
func cliFailure(engine string, timeout time.Duration, res *ExecResult, err error) *Error {
	switch {
	case errors.Is(err, ErrSandboxUnavailable):
		return Errf(false, jobs.ErrEngineUnavailable, "engine sandbox requested but unavailable: %v", err)
	case res == nil:
		// The parser never started (binary missing or not executable): a property of this
		// worker, not of the document.
		return Errf(true, jobs.ErrEngineUnavailable, "%s could not be started: %v", engine, err)
	case res.TimedOut:
		return Errf(true, jobs.ErrTimeout, "%s timed out after %s", engine, timeout)
	case errors.Is(err, context.Canceled):
		// The caller stopped the parse (job cancelled, lease lost, shutdown). The worker
		// decides what to report; this code only keeps it from reading as a bad file.
		return Errf(true, jobs.ErrCancelled, "%s was stopped because the parse was cancelled", engine)
	case res.OOM || errors.Is(err, ErrOOM):
		return Errf(true, jobs.ErrEngineOOM,
			"%s exceeded its memory ceiling while parsing this document", engine)
	case res.KilledBySignal:
		return Errf(true, jobs.ErrEngineOOM,
			"%s was killed by a SIGKILL ParseDock did not send (exit %d); this is almost always the kernel or cgroup OOM killer",
			engine, res.ExitCode)
	}
	code, msg := ClassifyCLIError(string(res.Stderr))
	return &Error{Code: code, Message: fmt.Sprintf("%s exit %d: %s", engine, res.ExitCode, msg),
		Retryable: code == jobs.ErrEngineOOM}
}

// classifierMessageMax bounds the message shown to clients; classification itself scans
// all of the (already 1 MiB-bounded) stderr.
const classifierMessageMax = 600

var (
	// logLevelRe finds a log-level token near the start of a line: "WARN x", "[WARN] x",
	// "WARN: x", "WARNING:root:x", "2026-...Z  INFO x".
	logLevelRe = regexp.MustCompile(`^(?:\S+\s+){0,3}?\[?(TRACE|DEBUG|INFO|WARN|WARNING)\]?(?:[:\s]|$)`)
	// errorWordRe marks a line as an error even when a level word appears later in it
	// ("Error: missing INFO dictionary").
	errorWordRe = regexp.MustCompile(`(?i)\b(error|fatal|panic|critical|caused by)\b`)
	// pathishRe matches tokens that are file names or paths, so a document called
	// "encrypted-report.pdf" or a directory named "unsupported" cannot steer the code.
	pathishRe  = regexp.MustCompile(`'[^']*'|"[^"]*"|\S*[/\\]\S*|\S+\.[A-Za-z0-9]{1,5}\b`)
	oomRe      = regexp.MustCompile(`memory allocation of \d+ bytes failed|out of memory|cannot allocate memory|bad_alloc|memoryerror`)
	timeoutRe  = regexp.MustCompile(`timed out|deadline exceeded`)
	noCryptRe  = regexp.MustCompile(`\b(not|isn't|is not|wasn't|was not)\s+(password[- ]protected|encrypted)\b|\bno password\b|\bwithout (a )?password\b`)
	cryptRe    = regexp.MustCompile(`\b(password|passphrase|encrypted|encryption|decrypt|decryption)\b`)
	unsuppRe   = regexp.MustCompile(`\bunsupported\b|\bnot supported\b`)
	expandedRe = regexp.MustCompile(`archive too large|exceeds docling_rs_sheet_max_cells|document_too_large`)
)

// isLogNoise reports whether a stderr line is an informational log line rather than
// the error itself.
func isLogNoise(line string) bool {
	m := logLevelRe.FindStringSubmatchIndex(line)
	if m == nil {
		return false
	}
	if e := errorWordRe.FindStringIndex(line); e != nil && e[0] < m[2] {
		return false
	}
	return true
}

// ClassifyCLIError turns a parser's stderr into a stable error code plus a short,
// useful message. Log noise (WARN/INFO lines, timestamps) is dropped: the previous
// "tail 400 bytes" approach truncated into the middle of a warning and hid the
// actual cause in a real-corpus run (PR-01 follow-up). The whole kept text is
// classified; only the returned message is shortened.
func ClassifyCLIError(stderr string) (code string, message string) {
	lines := strings.Split(stderr, "\n")
	keep := make([]string, 0, len(lines))
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" || isLogNoise(l) {
			continue
		}
		keep = append(keep, l)
	}
	full := strings.Join(keep, " | ")
	if full == "" {
		full = strings.TrimSpace(stderr)
	}
	msg := tailRunes(full, classifierMessageMax)
	low := strings.ToLower(full)
	// Keywords are matched with file names and quoted strings removed, and with explicit
	// negations ("not encrypted") taken out first.
	words := pathishRe.ReplaceAllString(low, " ")
	switch {
	case expandedRe.MatchString(low):
		return jobs.ErrExpandedTooLarge, "document expands beyond the parser limits: " + msg
	case oomRe.MatchString(words):
		return jobs.ErrEngineOOM, msg
	case timeoutRe.MatchString(words):
		return jobs.ErrTimeout, msg
	case cryptRe.MatchString(noCryptRe.ReplaceAllString(words, " ")):
		return jobs.ErrEncrypted, msg
	case unsuppRe.MatchString(words):
		return jobs.ErrUnsupportedType, msg
	}
	return jobs.ErrMalformed, msg
}

// tailRunes keeps the last n bytes of s without splitting a UTF-8 sequence.
func tailRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

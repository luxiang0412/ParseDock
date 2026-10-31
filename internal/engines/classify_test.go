package engines

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
)

func TestClassifyCLIErrorTightened(t *testing.T) {
	cases := []struct {
		name string
		in   string
		code string
	}{
		{"rust allocator abort", "memory allocation of 8589934592 bytes failed\n", jobs.ErrEngineOOM},
		{"python MemoryError", "Traceback (most recent call last):\nMemoryError\n", jobs.ErrEngineOOM},
		{"negated encryption is not encryption", "Error: invalid xref table (document is not encrypted)", jobs.ErrMalformed},
		{"path mentioning encrypted", "Error: cannot open /data/encrypted/inbox/a.pdf: unexpected EOF", jobs.ErrMalformed},
		{"file name mentioning password", "Error: failed to extract input 'password-list.xlsx': bad zip header", jobs.ErrMalformed},
		{"directory named unsupported", "Error: /srv/unsupported/x.docx: truncated archive", jobs.ErrMalformed},
		{"real encryption", "Error: PDF is encrypted and requires a password", jobs.ErrEncrypted},
		{"WARN: form is noise", "WARN: falling back to unsupported glyph table\nError: unexpected end of stream", jobs.ErrMalformed},
		{"[WARN] form is noise", "[WARN] encrypted metadata stream ignored\nError: bad trailer", jobs.ErrMalformed},
		{"python WARNING form is noise", "WARNING:root:password field ignored\nValueError: bad object", jobs.ErrMalformed},
		{"error line with a level word is kept", "Error: unsupported INFO dictionary layout", jobs.ErrUnsupportedType},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, msg := ClassifyCLIError(c.in); code != c.code {
				t.Fatalf("code = %s (%q), want %s", code, msg, c.code)
			}
		})
	}
}

// The cause can sit early in a long stderr; classification must see all of it, while the
// message stays bounded.
func TestClassifyCLIErrorScansFullStderr(t *testing.T) {
	in := "Error: Archive too large: 743948692 bytes\n" + strings.Repeat("Caused by: some long context line\n", 200)
	code, msg := ClassifyCLIError(in)
	if code != jobs.ErrExpandedTooLarge {
		t.Fatalf("code = %s, want %s", code, jobs.ErrExpandedTooLarge)
	}
	if len(msg) > classifierMessageMax+100 {
		t.Fatalf("message not bounded: %d bytes", len(msg))
	}
}

func TestCLIFailureMapping(t *testing.T) {
	cases := []struct {
		name      string
		res       *ExecResult
		err       error
		code      string
		retryable bool
	}{
		{"binary missing", nil, errors.New("exec: not found"), jobs.ErrEngineUnavailable, true},
		{"sandbox", nil, ErrSandboxUnavailable, jobs.ErrEngineUnavailable, false},
		{"our timeout", &ExecResult{TimedOut: true, ExitCode: -1}, context.DeadlineExceeded, jobs.ErrTimeout, true},
		{"caller cancel", &ExecResult{ExitCode: -1}, context.Canceled, jobs.ErrCancelled, true},
		{"rss monitor", &ExecResult{OOM: true, ExitCode: -1}, ErrOOM, jobs.ErrEngineOOM, true},
		// Kernel/cgroup OOM killer: SIGKILL, exit -1, no stderr. This used to fall through
		// to malformed_file and fail the job permanently.
		{"external SIGKILL", &ExecResult{KilledBySignal: true, ExitCode: -1}, errors.New("signal: killed"), jobs.ErrEngineOOM, true},
		{"stderr allocator abort", &ExecResult{ExitCode: 134, Stderr: []byte("memory allocation of 1024 bytes failed")}, errors.New("exit status 134"), jobs.ErrEngineOOM, true},
		{"plain failure", &ExecResult{ExitCode: 1, Stderr: []byte("Error: bad header")}, errors.New("exit status 1"), jobs.ErrMalformed, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := cliFailure("xberg", time.Minute, c.res, c.err)
			if e.Code != c.code || e.Retryable != c.retryable {
				t.Fatalf("got %s retryable=%v (%s), want %s retryable=%v", e.Code, e.Retryable, e.Message, c.code, c.retryable)
			}
		})
	}
}

// A real external SIGKILL flows through RunCLI and the shared mapping as an OOM kill.
func TestCLIFailureExternalKillEndToEnd(t *testing.T) {
	res, err := RunCLI(context.Background(), CLISpec{Bin: "/bin/sh"}, t.TempDir(), "-c", "kill -9 $$")
	if e := cliFailure("docling-rs", time.Minute, res, err); e.Code != jobs.ErrEngineOOM {
		t.Fatalf("code = %s (%s), want %s", e.Code, e.Message, jobs.ErrEngineOOM)
	}
}

// Review finding: Probe dereferenced a nil ExecResult when the binary was missing and
// panicked the worker.
func TestMinerUProbeMissingBinaryDoesNotPanic(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-mineru")
	if _, err := exec.LookPath(missing); err == nil {
		t.Skip("unexpectedly found")
	}
	err := (&MinerU{Bin: missing}).Probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err = %v", err)
	}
}

package engines

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// PR-04: a parser killed by the RSS ceiling must be reported as OOM, never as a
// clean exit with valid output (codex review finding).
func TestRunCLIReportsOOM(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available to allocate memory")
	}
	// Print valid JSON first (so the adapters' success recovery would be tempted),
	// then allocate well above the ceiling.
	script := `import sys,time
print('{"result":{"content":"looks-like-success"}}')
sys.stdout.flush()
b = bytearray(300 * 1024 * 1024)
time.sleep(5)`
	res, err := RunCLI(context.Background(), CLISpec{
		Bin: py, MaxOutput: 1 << 20, MaxRSSMB: 64,
	}, t.TempDir(), "-c", script)
	if !errors.Is(err, ErrOOM) {
		t.Fatalf("err = %v, want ErrOOM", err)
	}
	if res == nil {
		t.Fatal("no ExecResult on OOM")
	}
	if !res.OOM {
		t.Fatal("OOM flag not set")
	}
	if res.ExitCode == 0 {
		t.Fatal("exit code 0 after an OOM kill would be read as success")
	}
	if !strings.Contains(string(res.Stdout), "looks-like-success") {
		t.Fatalf("expected partial stdout, got %q", res.Stdout)
	}
}

// The ceiling must not affect a parser that stays below it.
func TestRunCLIToleratesProcessUnderCeiling(t *testing.T) {
	res, err := RunCLI(context.Background(), CLISpec{
		Bin: "/bin/sh", MaxOutput: 1 << 20, MaxRSSMB: 512,
	}, t.TempDir(), "-c", "printf hello; sleep 0.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OOM {
		t.Fatal("false OOM")
	}
	if string(res.Stdout) != "hello" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if res.Elapsed < 400*time.Millisecond {
		t.Fatalf("elapsed = %v", res.Elapsed)
	}
}

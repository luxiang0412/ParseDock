package engines

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Review finding: a grandchild that inherits stdout must not hold Wait open past the
// timeout, and a parser that exited by itself must not be reported as timed out.
func TestRunCLIGrandchildHoldingPipeIsNotATimeout(t *testing.T) {
	old := cliWaitDelay
	cliWaitDelay = 300 * time.Millisecond
	t.Cleanup(func() { cliWaitDelay = old })

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	res, err := RunCLI(ctx, CLISpec{Bin: "/bin/sh"}, dir, "-c", "sleep 6 & echo $! > "+pidFile+"; echo out")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("err = %v, want nil (the parser exited 0)", err)
	}
	if res.TimedOut {
		t.Fatal("TimedOut set for a parser that exited on its own")
	}
	if strings.TrimSpace(string(res.Stdout)) != "out" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("RunCLI blocked for %v on a leftover grandchild", elapsed)
	}
	// The leftover must have been killed with the process group.
	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatal(rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid <= 0 {
		t.Fatalf("bad pid %q", raw)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d still alive after RunCLI returned", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunCLIRealTimeoutIsFlagged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := RunCLI(ctx, CLISpec{Bin: "/bin/sh"}, t.TempDir(), "-c", "sleep 5")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if res == nil || !res.TimedOut {
		t.Fatalf("TimedOut not set: %+v", res)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout took %v", time.Since(start))
	}
}

// A caller cancellation is not a timeout.
func TestRunCLICancelIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	res, err := RunCLI(ctx, CLISpec{Bin: "/bin/sh"}, t.TempDir(), "-c", "sleep 5")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
	if res == nil || res.TimedOut {
		t.Fatalf("cancel reported as timeout: %+v", res)
	}
}

// A SIGKILL that RunCLI did not send (kernel/cgroup OOM killer) is recorded.
func TestRunCLIExternalSIGKILLIsRecorded(t *testing.T) {
	res, err := RunCLI(context.Background(), CLISpec{Bin: "/bin/sh"}, t.TempDir(), "-c", "kill -9 $$")
	if err == nil {
		t.Fatal("killed parser reported success")
	}
	if res == nil || !res.KilledBySignal {
		t.Fatalf("KilledBySignal not set: %+v", res)
	}
	if res.TimedOut || res.OOM {
		t.Fatalf("external kill mistaken for our own: %+v", res)
	}
}

// Review finding: with --sandbox unshare-net the resolved (absolute) binary must be
// handed to unshare, which runs inside the attempt directory.
func TestRunCLISandboxUsesResolvedBinary(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "fakebin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// A stand-in for util-linux unshare: drop "-n" and "--", then exec the rest.
	fake := "#!/bin/sh\n[ \"$1\" = -n ] && shift\n[ \"$1\" = -- ] && shift\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "unshare"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	unshareOnce, unshareErr = sync.Once{}, nil
	t.Cleanup(func() { unshareOnce, unshareErr = sync.Once{}, nil })

	if err := os.WriteFile(filepath.Join(dir, "engine.sh"), []byte("#!/bin/sh\necho ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	work := filepath.Join(dir, "attempt")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := RunCLI(ctx, CLISpec{Bin: "./engine.sh", Sandbox: "unshare-net"}, work)
	if err != nil {
		stderr := ""
		if res != nil {
			stderr = string(res.Stderr)
		}
		t.Fatalf("run: %v (stderr %q)", err, stderr)
	}
	if strings.TrimSpace(string(res.Stdout)) != "ran" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

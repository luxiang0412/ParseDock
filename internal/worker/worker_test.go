package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

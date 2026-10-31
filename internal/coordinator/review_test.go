package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Review finding: the job lock table was a global sync.Map that never dropped an entry, and
// the cleaner locks every job directory on every pass, so it grew without bound.
func TestJobLocksAreReleasedAndExclusive(t *testing.T) {
	var tab jobLockTable
	const workers, rounds = 8, 200
	inside := 0
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				unlock := tab.lock("job_a")
				inside++ // the race detector flags this if the lock is not exclusive
				if inside != 1 {
					t.Errorf("two holders inside the job lock")
				}
				inside--
				unlock()
				unlock() // idempotent: a double release must not corrupt the refcount
			}
		}()
	}
	wg.Wait()
	if n := tab.size(); n != 0 {
		t.Fatalf("lock table kept %d entries after every holder released", n)
	}
}

func TestSweepOrphansDoesNotGrowLockTable(t *testing.T) {
	s := testService(t, nil)
	old := time.Now().Add(-time.Hour)
	for i := 0; i < 50; i++ {
		dir := filepath.Join(s.Store.Root, "inputs", "job_orphan_"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	rep := &CleanupReport{}
	if n := s.sweepOrphans(context.Background(), rep); n != 50 {
		t.Fatalf("removed %d orphan dirs, want 50 (errors: %v)", n, rep.Errors)
	}
	if n := s.jobLocks.size(); n != 0 {
		t.Fatalf("sweepOrphans left %d lock entries behind", n)
	}
}

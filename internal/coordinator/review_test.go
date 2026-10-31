package coordinator

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
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

// sseEvents opens the job's event stream and returns the SSE event names until the server
// closes the stream.
func sseEvents(t *testing.T, srv *Server, jobID, scope string) []string {
	t.Helper()
	hs := httptest.NewServer(srv.PublicMux())
	defer hs.Close()
	url := hs.URL + "/v1/jobs/" + jobID + "/events"
	if scope != "" {
		url += "?scope=" + scope
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("X-Parsedock-Owner", "owner-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var names []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if name, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			names = append(names, name)
		}
	}
	return names
}

func completeTask(t *testing.T, s *Service, jobID, token string) *jobs.Job {
	t.Helper()
	done, err := s.Complete(context.Background(), CompleteParams{JobID: jobID, Token: token, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte(`{}`)})
	if err != nil {
		t.Errorf("complete: %v", err)
	}
	return done
}

func claimNative(t *testing.T, s *Service) *ClaimedTask {
	t.Helper()
	task, err := s.Claim(context.Background(), WorkerCaps{WorkerID: "w1", Engines: []string{"native"}, Device: "cpu"})
	if err != nil || task == nil {
		t.Fatalf("claim: %v %v", task, err)
	}
	return task
}

// Review finding 1: a completion landing between the SSE loop's two reads ended the stream
// (terminal status, "no new events") before the `completed` event was delivered.
func TestSSEDeliversTerminalEventWhenCompletionRacesThePoll(t *testing.T) {
	s := testService(t, nil)
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	task := claimNative(t, s)
	srv := NewServer(s, "", slog.Default())
	srv.ssePollEvery = 5 * time.Millisecond
	var once sync.Once
	srv.ssePollHook = func() { once.Do(func() { completeTask(t, s, job.ID, task.LeaseToken) }) }

	names := sseEvents(t, srv, job.ID, "")
	idx := func(name string) int {
		for i, n := range names {
			if n == name {
				return i
			}
		}
		return -1
	}
	c, end := idx("completed"), idx("stream_end")
	if c < 0 || end < 0 || c > end {
		t.Fatalf("stream ended without the completed event first: %v", names)
	}
}

// Review finding 12: scope=processing is documented not to deliver retention events.
func TestSSEProcessingScopeSkipsRetentionEvents(t *testing.T) {
	s := testService(t, nil)
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	task := claimNative(t, s)
	done := completeTask(t, s, job.ID, task.LeaseToken)
	if _, err := s.Ack(context.Background(), job.ID, "owner-1", done.ResultVersion, "release"); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(s, "", slog.Default())
	srv.ssePollEvery = 5 * time.Millisecond
	names := strings.Join(sseEvents(t, srv, job.ID, "processing"), ",")
	if strings.Contains(names, "release_pending") {
		t.Fatalf("processing scope delivered a retention event: %s", names)
	}
	if !strings.Contains(names, "completed") || !strings.HasSuffix(names, "stream_end") {
		t.Fatalf("unexpected stream: %s", names)
	}
}

// Review finding 4: a cancel whose UPDATE lost the race against a completion answered 202
// with the completed job; JOB-LIFECYCLE §4 says a completed job's cancel is a 409.
func TestCancelLosingRaceToCompletionIsConflict(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	stale, err := s.GetJob(ctx, job.ID, "owner-1") // what Cancel read: still queued
	if err != nil {
		t.Fatal(err)
	}
	task := claimNative(t, s)
	completeTask(t, s, job.ID, task.LeaseToken) // the completion wins before the UPDATE

	if _, err := s.cancelActive(ctx, stale); AsError(err).Status != 409 || AsError(err).Code != jobs.ErrConflict {
		t.Fatalf("cancel after completion: want 409 conflict, got %v", err)
	}
	evs, err := s.Events(ctx, job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type == "cancelled" {
			t.Fatal("a cancel that lost the race recorded a cancelled event")
		}
	}
	got, _ := s.GetJob(ctx, job.ID, "owner-1")
	if got.ProcessingStatus != jobs.StatusCompleted || got.RetentionStatus != jobs.RetentionActive {
		t.Fatalf("job changed: %s/%s", got.ProcessingStatus, got.RetentionStatus)
	}
}

// Review finding 4: the attempt update is part of the cancel; if it fails, nothing commits.
func TestCancelRollsBackWhenAttemptUpdateFails(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	claimNative(t, s)
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER no_attempt_cancel BEFORE UPDATE OF state ON attempts
		WHEN NEW.state = 'cancelled' BEGIN SELECT RAISE(ABORT, 'attempts unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(ctx, job.ID, "owner-1"); AsError(err).Status != 500 {
		t.Fatalf("want 500 when the attempt cannot be cancelled, got %v", err)
	}
	got, _ := s.GetJob(ctx, job.ID, "owner-1")
	if got.ProcessingStatus != jobs.StatusRunning {
		t.Fatalf("job was cancelled while its attempt kept running: %s", got.ProcessingStatus)
	}
}

// Review finding 1: the terminal event is written in the completion's transaction, so a
// failure to record it leaves the job untouched instead of completed-without-event.
func TestCompletionEventIsAtomicWithStatus(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	task := claimNative(t, s)
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER no_completed_event BEFORE INSERT ON job_events
		WHEN NEW.type = 'completed' BEGIN SELECT RAISE(ABORT, 'event store unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, CompleteParams{JobID: job.ID, Token: task.LeaseToken, Engine: "native",
		Version: "1", Markdown: []byte("# hi"), Text: []byte("hi"), JSON: []byte(`{}`)}); err == nil {
		t.Fatal("completion succeeded although its event could not be written")
	}
	got, err := s.GetJob(ctx, job.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProcessingStatus != jobs.StatusRunning {
		t.Fatalf("status = %s; the state change must roll back with its event", got.ProcessingStatus)
	}
}

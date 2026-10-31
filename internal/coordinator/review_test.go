package coordinator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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

// Review finding 6: the final retention UPDATE had no `retention_status = 'deleting'` guard,
// so two passes that both got past "mark deleting" both finalized: storage was released twice
// and the event, audit line and counter were emitted twice.
func TestFinalizeDeletedIsCompareAndSet(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	task := claimNative(t, s)
	done := completeTask(t, s, job.ID, task.LeaseToken)
	if _, err := s.Ack(ctx, job.ID, "owner-1", done.ResultVersion, "release"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE jobs SET retention_status = 'deleting', retention_target = 'released'
		WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	rep := &CleanupReport{}
	first := s.finalizeDeleted(ctx, rep, job.ID, jobs.RetentionReleased)
	second := s.finalizeDeleted(ctx, rep, job.ID, jobs.RetentionReleased)
	if !first || second {
		t.Fatalf("finalize = %v then %v; want exactly one success (errors %v)", first, second, rep.Errors)
	}
	evs, _ := s.Events(ctx, job.ID, 0, 100)
	n := 0
	for _, e := range evs {
		if e.Type == "released" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d released events, want 1", n)
	}
}

// Review finding 6: the cleaner deleted job files without the per-job lock that completion
// holds while publishing.
func TestReclaimWaitsForJobLock(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	if _, err := s.DB.ExecContext(ctx, `UPDATE jobs SET processing_status = 'failed', retention_status = 'release_pending'
		WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	unlock := s.lockJob(job.ID) // a publication in progress
	finished := make(chan struct{})
	go func() {
		s.reclaimJob(ctx, &CleanupReport{}, pendingReclaim{id: job.ID, status: string(jobs.RetentionReleasePending)})
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("cleaner reclaimed a job while its publication lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(s.Store.InputPath(job.ID)); err != nil {
		t.Fatalf("files deleted under the job lock: %v", err)
	}
	unlock()
	<-finished
	if _, err := os.Stat(s.Store.InputPath(job.ID)); !os.IsNotExist(err) {
		t.Fatalf("input not removed after the lock was released: %v", err)
	}
}

// Review finding 6: Fail deleted files while the row was still release_pending (bypassing the
// 'deleting' state the cleaner and downloads arbitrate on).
func TestFailDeletesThroughDeletingState(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	task := claimNative(t, s)
	got, err := s.Fail(ctx, job.ID, task.LeaseToken, jobs.ErrEngineUnavailable, "parser crashed", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionStatus != jobs.RetentionDeleting {
		t.Fatalf("retention after Fail = %s, want deleting", got.RetentionStatus)
	}
	if _, err := os.Stat(s.Store.InputPath(job.ID)); !os.IsNotExist(err) {
		t.Fatalf("input still present: %v", err)
	}
	if rep := s.RunCleanup(ctx); rep.Released != 1 {
		t.Fatalf("cleaner did not finalize the failed job: %+v", rep)
	}
	got, _ = s.GetJob(ctx, job.ID, "")
	if got.RetentionStatus != jobs.RetentionReleased {
		t.Fatalf("final retention = %s", got.RetentionStatus)
	}
}

// Review finding 9: the storage-quota read at admission discarded its error, so an unreadable
// balance counted as 0 bytes and the upload was admitted (fail-open).
func TestStorageQuotaFailsClosedOnLookupError(t *testing.T) {
	s := testService(t, func(c *Config) { c.Quota = Quota{MaxStorageBytes: 1 << 20} })
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `DROP TABLE owner_storage`); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CreateJob(ctx, CreateParams{OwnerID: "owner-1", InputName: "a.txt", Body: stringsReader("hello")})
	if AsError(err).Status != 500 || AsError(err).Code != jobs.ErrInternal {
		t.Fatalf("admission with an unreadable storage balance: want 500 internal_error, got %v", err)
	}
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d job rows created although the quota could not be checked", n)
	}
}

// Review finding 9: the max-lifetime UPDATE (cleanup step 6) dropped its error, so the pass
// reported itself healthy while overdue jobs kept running.
func TestCleanupReportsMaxLifetimeFailure(t *testing.T) {
	s := testService(t, nil)
	ctx := context.Background()
	job := create(t, s, "a.txt", []byte("hello"), CreateParams{})
	if _, err := s.DB.ExecContext(ctx, `UPDATE jobs SET max_lifetime_at = ? WHERE id = ?`,
		ts(nowUTC().Add(-time.Minute)), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER no_timeout BEFORE UPDATE OF processing_status ON jobs
		WHEN NEW.error_code = 'timeout' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	rep := s.RunCleanup(ctx)
	found := false
	for _, e := range rep.Errors {
		if strings.HasPrefix(e, "max lifetime:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("step 6 failure not reported: %v", rep.Errors)
	}
}

// postJob submits a small text file to POST /v1/jobs with the given query string.
func postJob(t *testing.T, srv *Server, path, query string, body []byte) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(body)
	_ = mw.Close()
	hs := httptest.NewServer(srv.PublicMux())
	defer hs.Close()
	req, _ := http.NewRequest(http.MethodPost, hs.URL+path+"?"+query, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Parsedock-Owner", "owner-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// Review finding 2: Atoi errors were dropped and neither value was range-checked, so
// priority=999999 jumped every other tenant's queue and ttl_seconds=10000000000 overflowed
// time.Duration (negative TTL, the result expired at commit).
func TestCreateJobValidatesPriorityAndTTL(t *testing.T) {
	s := testService(t, nil)
	srv := NewServer(s, "", slog.Default())
	for _, q := range []string{"priority=abc", "priority=101", "priority=-1", "priority=999999",
		"ttl_seconds=1e10", "ttl_seconds=0", "ttl_seconds=-5", "ttl_seconds=soon"} {
		resp, body := postJob(t, srv, "/v1/jobs", q, []byte("hello"))
		if resp.StatusCode != 400 || errCode(body) != jobs.ErrInvalidRequest {
			t.Errorf("%s: got %d %v, want 400 invalid_request", q, resp.StatusCode, body)
		}
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d jobs created from invalid requests", n)
	}

	resp, body := postJob(t, srv, "/v1/jobs", "priority=100&ttl_seconds=10000000000", []byte("hello"))
	if resp.StatusCode != 202 {
		t.Fatalf("valid request refused: %d %v", resp.StatusCode, body)
	}
	jobID, _ := body["job_id"].(string)
	var stored int64
	_ = s.DB.QueryRow(`SELECT requested_ttl_seconds FROM jobs WHERE id = ?`, jobID).Scan(&stored)
	if want := int64(s.Cfg.MaxResultTTL / time.Second); stored != want {
		t.Fatalf("stored ttl %d, want it capped to %d", stored, want)
	}
	task := claimNative(t, s)
	done := completeTask(t, s, jobID, task.LeaseToken)
	if done.ResultExpiresAt == nil || !done.ResultExpiresAt.After(time.Now().Add(s.Cfg.MaxResultTTL-time.Minute)) {
		t.Fatalf("result_expires_at = %v, want about now + %s", done.ResultExpiresAt, s.Cfg.MaxResultTTL)
	}
}

func TestResultTTLDoesNotOverflow(t *testing.T) {
	s := testService(t, nil)
	for _, req := range []int64{1 << 40, 1 << 62, 1<<63 - 1} {
		if got := s.resultTTL(req); got != s.Cfg.MaxResultTTL {
			t.Fatalf("resultTTL(%d) = %s, want the ceiling %s", req, got, s.Cfg.MaxResultTTL)
		}
	}
	if got := s.resultTTL(90); got != 90*time.Second {
		t.Fatalf("resultTTL(90) = %s", got)
	}
	if got := s.resultTTL(0); got != s.Cfg.ResultTTL {
		t.Fatalf("resultTTL(0) = %s, want the default %s", got, s.Cfg.ResultTTL)
	}
}

// Review finding 10: sanitizeName cut at byte 180 and could split a multibyte rune.
func TestSanitizeNameKeepsValidUTF8(t *testing.T) {
	name := strings.Repeat("a", 179) + "文档.pdf" // the 180-byte boundary falls inside 文
	got := sanitizeName(name)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated name is not valid UTF-8: %q", got)
	}
	if len(got) > maxInputNameBytes || got != strings.Repeat("a", 179) {
		t.Fatalf("got %q (%d bytes)", got, len(got))
	}
	if got := sanitizeName("报告.docx"); got != "报告.docx" {
		t.Fatalf("short name altered: %q", got)
	}
}

// Review finding 10: an over-long idempotency key is a 400, not a silent truncation.
func TestCreateJobRejectsLongIdempotencyKey(t *testing.T) {
	s := testService(t, nil)
	long := strings.Repeat("k", jobs.MaxIdempotencyKeyLen)
	if _, _, err := s.CreateJob(context.Background(), CreateParams{OwnerID: "owner-1", InputName: "a.txt",
		IdempotencyKey: long + "1", Body: stringsReader("hello")}); AsError(err).Status != 400 ||
		AsError(err).Code != jobs.ErrInvalidRequest {
		t.Fatalf("201-character key: want 400 invalid_request, got %v", err)
	}
	first := create(t, s, "a.txt", []byte("hello"), CreateParams{IdempotencyKey: long})
	if first.IdempotencyKey != long {
		t.Fatalf("200-character key stored as %q", first.IdempotencyKey)
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

// Package worker implements the stateless parse worker: register, claim, fetch
// input, run one engine, stream the result back. All state lives in the
// Coordinator; a worker killed at any point is recovered by lease expiry.
package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/engines"
	"parsedock.dev/parsedock/internal/jobs"
	"parsedock.dev/parsedock/internal/sysinfo"
)

// Config is the worker configuration.
type Config struct {
	CoordinatorURL    string
	InternalToken     string
	WorkerID          string
	DeviceType        string // cpu | gpu
	GPUModel          string
	GPUMemoryBytes    int64
	MaxConcurrency    int
	WorkDir           string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	MaxParseTime      time.Duration
	MaxInputBytes     int64
	// WorkMaxAge is how long an attempt directory may live before the scrubber
	// removes it (crash leftovers). WorkScrubInterval is the scrubber period.
	WorkMaxAge        time.Duration
	WorkScrubInterval time.Duration
	Engines           []string // engine names this worker will accept
	// EscalateScans retries a CJK scan once with an accurate engine when the fast
	// engine warns about OCR quality (RC-3 from the real-corpus test).
	EscalateScans bool
	Host          string
}

// Worker is one process-level worker.
type Worker struct {
	Cfg  Config
	Reg  *engines.Registry
	Log  *slog.Logger
	HTTP *http.Client

	inFlight atomic.Int64
	claimed  atomic.Int64
	failed   atomic.Int64

	// activeScratch holds attempt directories currently in use, so the scrubber
	// never deletes a live attempt (F09).
	activeScratch sync.Map
}

// New builds a worker.
func New(cfg Config, reg *engines.Registry, log *slog.Logger) *Worker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 10 * time.Second
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 1
	}
	if cfg.WorkMaxAge <= 0 {
		cfg.WorkMaxAge = time.Hour
	}
	if cfg.WorkScrubInterval <= 0 {
		cfg.WorkScrubInterval = 10 * time.Minute
	}
	if cfg.DeviceType == "" {
		cfg.DeviceType = "cpu"
	}
	return &Worker{
		Cfg: cfg, Reg: reg, Log: log,
		HTTP: &http.Client{Timeout: 0},
	}
}

// Run registers the worker and keeps claim loops alive until ctx is done.
func (w *Worker) Run(ctx context.Context) error {
	// Recover scratch space left behind by a previous crash before claiming work.
	if n, err := SweepScratch(w.Cfg.WorkDir, w.Cfg.WorkMaxAge, w.isActiveScratch); err != nil {
		w.Log.Warn("startup scratch sweep failed", "err", err)
	} else if n > 0 {
		w.Log.Info("startup scratch sweep removed stale attempt dirs", "removed", n)
	}
	go w.scrubLoop(ctx)

	if err := w.register(ctx); err != nil {
		w.Log.Warn("initial registration failed; will retry", "err", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < w.Cfg.MaxConcurrency; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			w.loop(ctx, slot)
		}(i)
	}
	wg.Wait()
	return ctx.Err()
}

// scrubLoop periodically removes stale attempt directories.
func (w *Worker) scrubLoop(ctx context.Context) {
	t := time.NewTicker(w.Cfg.WorkScrubInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := SweepScratch(w.Cfg.WorkDir, w.Cfg.WorkMaxAge, w.isActiveScratch)
			if err != nil {
				w.Log.Warn("scratch sweep failed", "err", err)
				continue
			}
			if n > 0 {
				w.Log.Info("scratch sweep removed stale attempt dirs", "removed", n)
			}
		}
	}
}

func (w *Worker) isActiveScratch(dir string) bool {
	_, ok := w.activeScratch.Load(dir)
	return ok
}

// SweepScratch removes attempt directories under root that are older than maxAge
// and not currently in use. Layout is <root>/<job>/<attempt>/.
//
// It is exported so the behaviour is testable without starting a worker, and so
// an operator can run it from a maintenance job.
func SweepScratch(root string, maxAge time.Duration, isActive func(string) bool) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, jobEntry := range entries {
		if !jobEntry.IsDir() {
			continue
		}
		jobDir := filepath.Join(root, jobEntry.Name())
		attempts, err := os.ReadDir(jobDir)
		if err != nil {
			continue
		}
		for _, att := range attempts {
			if !att.IsDir() {
				continue
			}
			dir := filepath.Join(jobDir, att.Name())
			if isActive != nil && isActive(dir) {
				continue
			}
			info, err := att.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			if err := os.RemoveAll(dir); err == nil {
				removed++
			}
		}
		// Remove the job directory once it is empty. It only becomes empty after
		// its attempts are gone, so no age check is needed (and its mtime is
		// updated by the deletions above).
		if isActive != nil && isActive(jobDir) {
			continue
		}
		_ = os.Remove(jobDir) // fails harmlessly when still in use
	}
	return removed, nil
}

func (w *Worker) register(ctx context.Context) error {
	body := map[string]any{
		"worker_id":        w.Cfg.WorkerID,
		"host":             w.Cfg.Host,
		"engines":          w.Cfg.Engines,
		"device_type":      w.Cfg.DeviceType,
		"gpu_model":        w.Cfg.GPUModel,
		"gpu_memory_bytes": w.Cfg.GPUMemoryBytes,
		"free_vram_bytes":  w.freeVRAMBytes(),
		"cpu_cores":        numCPU(),
		"memory_bytes":     totalMemory(),
		"max_concurrency":  w.Cfg.MaxConcurrency,
	}
	var out struct {
		WorkerID string `json:"worker_id"`
	}
	if err := w.postJSON(ctx, "/internal/workers/register", body, &out); err != nil {
		return err
	}
	if w.Cfg.WorkerID == "" && out.WorkerID != "" {
		w.Cfg.WorkerID = out.WorkerID
		body["worker_id"] = out.WorkerID
	}
	free := w.freeVRAMBytes()
	w.Log.Info("worker registered", "worker_id", w.Cfg.WorkerID, "engines", w.Cfg.Engines,
		"device", w.Cfg.DeviceType, "concurrency", w.Cfg.MaxConcurrency,
		"free_vram_mb", free>>20, "vram_known", free >= 0)
	return nil
}

func (w *Worker) loop(ctx context.Context, slot int) {
	lastReg := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if time.Since(lastReg) > time.Minute {
			_ = w.register(ctx)
			lastReg = time.Now()
		}
		task, err := w.claim(ctx)
		if err != nil {
			w.Log.Warn("claim failed", "err", err)
			sleepJitter(ctx, 3*time.Second)
			continue
		}
		if task == nil {
			sleepJitter(ctx, w.Cfg.PollInterval)
			continue
		}
		w.claimed.Add(1)
		w.inFlight.Add(1)
		if err := w.execute(ctx, task); err != nil {
			w.failed.Add(1)
			w.Log.Warn("task failed", "job", task.JobID, "err", err)
		}
		w.inFlight.Add(-1)
	}
}

func (w *Worker) claim(ctx context.Context) (*coordClaim, error) {
	req := map[string]any{
		"worker_id":   w.Cfg.WorkerID,
		"engines":     w.Cfg.Engines,
		"device_type": w.Cfg.DeviceType,
	}
	var task coordClaim
	ok, err := w.postJSONOptional(ctx, "/internal/tasks/claim", req, &task)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &task, nil
}

type coordClaim struct {
	JobID         string    `json:"job_id"`
	AttemptNo     int       `json:"attempt_no"`
	LeaseToken    string    `json:"lease_token"`
	LeaseUntil    time.Time `json:"lease_until"`
	Format        string    `json:"format"`
	MimeType      string    `json:"mime_type"`
	InputName     string    `json:"input_name"`
	InputSHA256   string    `json:"input_sha256"`
	InputSize     int64     `json:"input_size"`
	PageCount     int       `json:"page_count"`
	Mode          string    `json:"requested_mode"`
	Device        string    `json:"requested_device"`
	EngineHint    string    `json:"engine_hint"`
	RoutingReason string    `json:"routing_reason"`
	MaxLifetimeAt time.Time `json:"max_lifetime_at"`
}

func (w *Worker) execute(ctx context.Context, task *coordClaim) error {
	workDir := filepath.Join(w.Cfg.WorkDir, task.JobID, fmt.Sprintf("a%d", task.AttemptNo))
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return w.fail(ctx, task, jobs.ErrInternal, "worker scratch dir: "+err.Error(), false)
	}
	w.activeScratch.Store(workDir, struct{}{})
	defer func() {
		w.activeScratch.Delete(workDir)
		_ = os.RemoveAll(workDir)
		// Best effort: drop the per-job directory once the attempt is gone.
		_ = os.Remove(filepath.Dir(workDir))
	}()

	// Hard deadline from the job's absolute max lifetime.
	deadline := time.Now().Add(w.Cfg.MaxParseTime)
	if !task.MaxLifetimeAt.IsZero() && task.MaxLifetimeAt.Before(deadline) {
		deadline = task.MaxLifetimeAt
	}
	parseCtx, cancelParse := context.WithDeadline(ctx, deadline)
	defer cancelParse()

	local := filepath.Join(workDir, "input."+task.Format)
	if err := w.downloadInput(parseCtx, task, local); err != nil {
		return w.fail(ctx, task, jobs.ErrInternal, "download input: "+err.Error(), true)
	}

	engine, err := w.Reg.Pick(task.EngineHint, task.Format, task.Mode)
	if err != nil {
		return w.fail(ctx, task, jobs.ErrUnsupportedType, err.Error(), false)
	}
	if task.Device == jobs.DeviceGPURequired && engine.Device() == "cpu" {
		return w.fail(ctx, task, jobs.ErrGPUUnavailable, "gpu_required job cannot run on a CPU engine", false)
	}

	var progress atomic.Int64
	progress.Store(0)
	stage := atomic.Value{}
	stage.Store("starting")
	hbCtx, cancelHB := context.WithCancel(parseCtx)
	defer cancelHB()
	var hbErr atomic.Value
	go func() {
		t := time.NewTicker(w.Cfg.HeartbeatInterval)
		defer t.Stop()
		last := -1
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				p := int(progress.Load())
				st, _ := stage.Load().(string)
				if p == last && st == "" {
					continue
				}
				last = p
				if err := w.heartbeat(hbCtx, task, p, st); err != nil {
					hbErr.Store(err)
					cancelHB()
					cancelParse()
					return
				}
			}
		}
	}()

	result, perr := engine.Parse(parseCtx, engines.Request{
		JobID: task.JobID, Path: local, Format: task.Format, MimeType: task.MimeType,
		Mode: task.Mode, Device: w.Cfg.DeviceType, WorkDir: workDir,
		Timeout: deadline.Sub(time.Now()), PageLimit: 0,
		Progress: func(p int, s string) {
			progress.Store(int64(p))
			stage.Store(s)
		},
	})
	// Enrichment and escalation both run inside the heartbeat-protected phase: a
	// large workbook can take a while to unzip, and a lost lease would let the
	// coordinator reclaim a job that is still being processed.
	if perr == nil && engines.ShouldEnrichFormulas(task.Format) {
		n, warns, eerr := engines.EnrichXLSXFormulas(parseCtx, local, result)
		if eerr != nil {
			// Enrichment is best effort only for genuine parse problems; a
			// cancelled/expired context must fail the task instead of uploading a
			// half-enriched result after the deadline.
			if parseCtx.Err() != nil {
				if e, ok := hbErr.Load().(error); ok && e != nil && isCancelled(e) {
					w.Log.Info("task cancelled during formula enrichment", "job", task.JobID)
					return nil
				}
				return w.fail(ctx, task, jobs.ErrTimeout,
					"parse deadline exceeded during formula enrichment", true)
			}
			w.Log.Warn("formula enrichment failed", "job", task.JobID, "err", eerr)
		} else {
			result.Warnings = append(result.Warnings, warns...)
			if n > 0 {
				w.Log.Info("formula enrichment", "job", task.JobID, "cells", n)
			}
		}
	}
	if perr == nil {
		result, engine = w.maybeEscalate(parseCtx, task, engine, result, workDir, local)
	}
	cancelHB()
	if e, ok := hbErr.Load().(error); ok && e != nil && isCancelled(e) {
		// The job was cancelled while the engine was running: stop here even if
		// the engine happened to finish, otherwise we would race the cleanup.
		w.Log.Info("task cancelled by coordinator", "job", task.JobID)
		return nil
	}
	if perr != nil {
		if e, ok := hbErr.Load().(error); ok && e != nil {
			// A cancelled job is a clean stop, not a failure: the coordinator has
			// already moved it to `cancelled` and marked this attempt cancelled.
			if isCancelled(e) {
				w.Log.Info("task cancelled by coordinator", "job", task.JobID)
				return nil
			}
			return w.fail(ctx, task, jobs.ErrCancelled, e.Error(), false)
		}
		var ee *engines.Error
		if errors.As(perr, &ee) {
			return w.fail(ctx, task, ee.Code, ee.Message, ee.Retryable)
		}
		return w.fail(ctx, task, jobs.ErrInternal, perr.Error(), true)
	}
	// Legacy OLE2 Office keeps text and tables only; say so instead of letting a
	// consumer assume page geometry exists.
	if isLegacyFormat(task.Format) {
		result.AddWarning("legacy_format",
			"legacy OLE2 Office document: text and tables are extracted, page numbers and coordinates are not available", "info")
	}

	result.Source.FileName = task.InputName
	result.Source.SHA256 = task.InputSHA256
	result.Source.SizeBytes = task.InputSize
	if result.Source.Format == "" {
		result.Source.Format = task.Format
	}
	if result.Source.MimeType == "" {
		result.Source.MimeType = task.MimeType
	}
	result.ParserEngine = engine.Name()
	result.ParserVersion = engine.Version()
	result.Routing.RequestedMode = task.Mode
	result.Routing.RequestedDevice = task.Device
	result.Routing.ActualDevice = engine.Device()
	result.Routing.Engine = engine.Name()
	if result.Routing.Reason == "" {
		result.Routing.Reason = task.RoutingReason
	}
	result.Finalize()

	size := int64(0)
	for _, el := range result.Elements {
		size += int64(len(el.Text))
	}
	if size == 0 {
		return w.fail(ctx, task, jobs.ErrMalformed, "engine produced no text", false)
	}
	if err := w.uploadResult(ctx, task, engine, result); err != nil {
		return err
	}
	w.Log.Info("task completed", "job", task.JobID, "engine", engine.Name(),
		"pages", result.PageCount, "elements", len(result.Elements))
	return nil
}

// maybeEscalate retries a poor OCR result once with an accurate engine. The
// replacement is only accepted when it yields at least as much text, and the
// fallback is recorded in the IR routing so downstream quality checks can see it.
func (w *Worker) maybeEscalate(ctx context.Context, task *coordClaim, used engines.Engine,
	doc *documentir.Document, workDir, inputPath string) (*documentir.Document, engines.Engine) {
	if !w.Cfg.EscalateScans || doc == nil {
		return doc, used
	}
	if !engines.ShouldEscalateToAccurate(task.Mode, task.Format, doc) {
		return doc, used
	}
	alt, err := w.Reg.Pick("", task.Format, "accurate")
	if err != nil || alt.Name() == used.Name() {
		return doc, used
	}
	w.Log.Info("escalating OCR result to accurate engine", "job", task.JobID,
		"from", used.Name(), "to", alt.Name())
	res, perr := alt.Parse(ctx, engines.Request{
		// Always retry from the worker's own input copy, never from an engine's
		// scratch copy (which the first engine may have already overwritten).
		JobID: task.JobID, Path: inputPath,
		Format: task.Format, MimeType: task.MimeType, Mode: task.Mode,
		Device: w.Cfg.DeviceType, WorkDir: workDir, Timeout: 0,
		Progress: func(int, string) {},
	})
	if perr != nil || res == nil {
		w.Log.Warn("escalation failed; keeping the fast result", "job", task.JobID, "err", perr)
		return doc, used
	}
	if res.TextLength() < doc.TextLength() {
		w.Log.Info("escalation produced less text; keeping the fast result", "job", task.JobID)
		return doc, used
	}
	res.Routing.Fallback = true
	res.Routing.Reason = engines.EscalationReason(used.Name(), alt.Name(), engines.CJKRatio(doc))
	res.Routing.RequestedMode = task.Mode
	res.Routing.RequestedDevice = task.Device
	res.Routing.ActualDevice = alt.Device()
	res.Routing.Engine = alt.Name()
	res.Metrics.ParseMs += doc.Metrics.ParseMs
	return res, alt
}

func isLegacyFormat(format string) bool {
	switch format {
	case "doc", "xls", "ppt":
		return true
	}
	return false
}

func (w *Worker) downloadInput(ctx context.Context, task *coordClaim, dst string) error {
	req, err := w.newRequest(ctx, http.MethodGet, "/internal/tasks/"+task.JobID+"/input", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Lease-Token", task.LeaseToken)
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("input fetch %s: %s", resp.Status, string(raw))
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	limit := w.Cfg.MaxInputBytes
	if limit <= 0 {
		limit = 1 << 30
	}
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	cerr := f.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if n > limit {
		return fmt.Errorf("input exceeds %d bytes", limit)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if task.InputSHA256 != "" && sum != task.InputSHA256 {
		return fmt.Errorf("input checksum mismatch: got %s want %s", sum, task.InputSHA256)
	}
	return nil
}

func (w *Worker) uploadResult(ctx context.Context, task *coordClaim, engine engines.Engine, doc *documentir.Document) error {
	payload := map[string]any{
		"lease_token":    task.LeaseToken,
		"engine":         engine.Name(),
		"engine_version": engine.Version(),
		"device_type":    w.Cfg.DeviceType,
		"page_count":     doc.PageCount,
		"document":       doc,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return w.fail(ctx, task, jobs.ErrInternal, "encode result: "+err.Error(), false)
	}
	req, err := w.newRequest(ctx, http.MethodPut, "/internal/tasks/"+task.JobID+"/result", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("upload result: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("upload result rejected %s: %s", resp.Status, string(raw))
	}
	return nil
}

func (w *Worker) heartbeat(ctx context.Context, task *coordClaim, progress int, stage string) error {
	body := map[string]any{"lease_token": task.LeaseToken, "progress_percent": progress, "stage": stage}
	var out struct {
		LeaseUntil time.Time `json:"lease_until"`
	}
	return w.postJSON(ctx, "/internal/tasks/"+task.JobID+"/heartbeat", body, &out)
}

func (w *Worker) fail(ctx context.Context, task *coordClaim, code, msg string, retryable bool) error {
	body := map[string]any{"lease_token": task.LeaseToken, "code": code, "message": msg, "retryable": retryable}
	// Use a background context: the parse context may already be cancelled.
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var out map[string]any
	if err := w.postJSON(c, "/internal/tasks/"+task.JobID+"/fail", body, &out); err != nil {
		w.Log.Warn("fail report rejected", "job", task.JobID, "err", err)
		return fmt.Errorf("%s: %s (report failed: %v)", code, msg, err)
	}
	return fmt.Errorf("%s: %s", code, msg)
}

func (w *Worker) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, w.Cfg.CoordinatorURL+path, body)
	if err != nil {
		return nil, err
	}
	if w.Cfg.InternalToken != "" {
		req.Header.Set("X-Internal-Token", w.Cfg.InternalToken)
	}
	return req, nil
}

func (w *Worker) postJSON(ctx context.Context, path string, in any, out any) error {
	_, err := w.postJSONOptional(ctx, path, in, out)
	return err
}

func (w *Worker) postJSONOptional(ctx context.Context, path string, in any, out any) (bool, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return false, err
	}
	req, err := w.newRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return false, nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return false, fmt.Errorf("%s: %s", resp.Status, truncate(string(raw), 300))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return true, err
		}
	}
	return true, nil
}

// isCancelled reports whether the coordinator rejected the heartbeat because the
// job was cancelled (as opposed to the lease being lost).
func isCancelled(err error) bool {
	return strings.Contains(err.Error(), jobs.ErrCancelled)
}

func sleepJitter(ctx context.Context, d time.Duration) {
	jitter := time.Duration(rand.Int63n(int64(d/2) + 1))
	select {
	case <-ctx.Done():
	case <-time.After(d + jitter):
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func numCPU() int        { return sysinfo.CPU() }
func totalMemory() int64 { return sysinfo.MemoryBytes() }

// freeVRAMBytes reports the currently free GPU memory, or -1 when unknown. It is
// queried on every register/heartbeat so admission uses fresh data instead of the
// value captured at start-up.
func (w *Worker) freeVRAMBytes() int64 {
	if w.Cfg.DeviceType != "gpu" {
		return -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if mb, ok := sysinfo.FreeVRAMMB(ctx); ok {
		return mb << 20
	}
	return -1
}

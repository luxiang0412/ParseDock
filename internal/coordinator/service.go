package coordinator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
	"parsedock.dev/parsedock/internal/storage"
)

const jobCols = `id, owner_id, input_name, mime_type, format, input_sha256, input_size, page_count,
	processing_status, retention_status, priority, requested_mode, requested_device, engine_hint,
	actual_engine, routing_reason, idempotency_key, created_at, started_at, completed_at,
	result_expires_at, max_lifetime_at, current_attempt, max_attempts, result_version,
	result_manifest_path, error_code, error_message, warning_count, progress, progress_stage,
	requested_ttl_seconds, available_at, tombstone_expires_at, updated_at`

// Service is the Coordinator: the only component that touches SQLite and the
// authoritative file store.
type Service struct {
	DB    *sql.DB
	Store *storage.Store
	Cfg   Config

	cleanupRuns atomic.Int64
	lastCleanup atomic.Value // time.Time
}

// NewService wires the coordinator.
func NewService(db *sql.DB, store *storage.Store, cfg Config) *Service {
	s := &Service{DB: db, Store: store, Cfg: cfg}
	s.lastCleanup.Store(time.Time{})
	return s
}

// ---------------------------------------------------------------- job creation

// CreateParams is one submission.
type CreateParams struct {
	OwnerID        string
	InputName      string
	MimeType       string
	Mode           string
	Device         string
	Priority       int
	IdempotencyKey string
	TTLSeconds     int
	Body           io.Reader
}

// CreateJob streams the upload, validates it, routes it and persists a queued job.
// The returned bool reports whether a new job was created (false = idempotent replay).
func (s *Service) CreateJob(ctx context.Context, p CreateParams) (*jobs.Job, bool, error) {
	jobID := jobs.NewJobID()
	if s.Cfg.MaxUploadBytes > 0 {
		if err := s.Store.CheckSpace(s.Cfg.MaxUploadBytes); err != nil {
			return nil, false, NewError(507, jobs.ErrInsufficientSpace, "storage below safety watermark")
		}
	}
	writer, err := s.Store.NewInputWriter(jobID, s.Cfg.MaxUploadBytes)
	if err != nil {
		return nil, false, NewError(507, jobs.ErrInsufficientSpace, err.Error())
	}
	if _, err := io.Copy(writer, p.Body); err != nil {
		writer.Abort()
		if errors.Is(err, storage.ErrTooLarge) {
			return nil, false, NewError(413, jobs.ErrTooLarge,
				fmt.Sprintf("upload exceeds %d bytes", s.Cfg.MaxUploadBytes))
		}
		return nil, false, NewError(400, jobs.ErrInvalidRequest, "upload interrupted: "+err.Error())
	}
	return s.CreateJobStaged(ctx, p, jobID, writer)
}

// StageInput prepares a streaming writer for an upload whose metadata is not
// known yet. The HTTP layer uses it to accept multipart parts in any order
// without buffering the document in memory (F06).
func (s *Service) StageInput(jobID string) (*storage.InputWriter, error) {
	if s.Cfg.MaxUploadBytes > 0 {
		if err := s.Store.CheckSpace(s.Cfg.MaxUploadBytes); err != nil {
			return nil, NewError(507, jobs.ErrInsufficientSpace, "storage below safety watermark")
		}
	}
	w, err := s.Store.NewInputWriter(jobID, s.Cfg.MaxUploadBytes)
	if err != nil {
		return nil, NewError(507, jobs.ErrInsufficientSpace, err.Error())
	}
	return w, nil
}

// CreateJobStaged validates an already-staged upload and creates the job.
// Idempotency is evaluated here because the key may arrive after the file.
func (s *Service) CreateJobStaged(ctx context.Context, p CreateParams, jobID string, writer *storage.InputWriter) (*jobs.Job, bool, error) {
	mode, err := jobs.ValidateMode(p.Mode)
	if err != nil {
		writer.Abort()
		return nil, false, NewError(400, jobs.ErrInvalidRequest, err.Error())
	}
	device, err := jobs.ValidateDevice(p.Device)
	if err != nil {
		writer.Abort()
		return nil, false, NewError(400, jobs.ErrInvalidRequest, err.Error())
	}
	if p.OwnerID == "" {
		writer.Abort()
		return nil, false, NewError(401, jobs.ErrUnauthorized, "missing owner")
	}
	if p.IdempotencyKey != "" {
		key := jobs.NormalizeIdempotencyKey(p.IdempotencyKey)
		if existing, err := s.findByIdemKey(ctx, p.OwnerID, key); err == nil && existing != nil {
			writer.Abort() // the staged bytes are a duplicate
			return existing, false, nil
		}
	}
	if writer.Size() == 0 {
		writer.Abort()
		return nil, false, NewError(400, jobs.ErrMalformed, "empty upload")
	}

	lim := s.limits()
	// Validate before publishing so a rejected upload never has a job row.
	if err := s.validateStaged(jobID, writer, p.InputName, lim); err != nil {
		writer.Abort()
		return nil, false, err
	}
	size, sha, err := s.Store.CommitInput(writer, jobID, map[string]any{
		"input_name": p.InputName,
		"mime_type":  p.MimeType,
		"owner_id":   p.OwnerID,
	})
	if err != nil {
		return nil, false, NewError(500, jobs.ErrInternal, "commit input: "+err.Error())
	}

	format, verr := sniffCommitted(s.Store.InputPath(jobID), p.InputName, lim)
	if verr != nil {
		_ = s.Store.DeleteJob(jobID)
		return nil, false, verr
	}
	pageCount := 0
	if format.Name == "pdf" {
		pages, encrypted, perr := storage.CheckPDF(s.Store.InputPath(jobID), lim)
		pageCount = pages
		if perr != nil {
			_ = s.Store.DeleteJob(jobID)
			return nil, false, validationErr(perr)
		}
		if encrypted {
			_ = s.Store.DeleteJob(jobID)
			return nil, false, NewError(422, jobs.ErrEncrypted, "password-protected PDF is not supported in Phase 0")
		}
	}
	if format.ZipBased {
		if err := storage.VerifyZipContainer(s.Store.InputPath(jobID), format, lim); err != nil {
			_ = s.Store.DeleteJob(jobID)
			return nil, false, validationErr(err)
		}
	}

	engineHint, reason, gerr := s.route(ctx, format.Name, mode, device)
	if gerr != nil {
		_ = s.Store.DeleteJob(jobID)
		return nil, false, gerr
	}

	now := nowUTC()
	ttl := p.TTLSeconds
	if ttl < 0 {
		ttl = 0
	}
	job := &jobs.Job{
		ID:               jobID,
		OwnerID:          p.OwnerID,
		InputName:        sanitizeName(p.InputName),
		MimeType:         format.Mime,
		Format:           format.Name,
		InputSHA256:      sha,
		InputSize:        size,
		PageCount:        pageCount,
		ProcessingStatus: jobs.StatusQueued,
		RetentionStatus:  jobs.RetentionActive,
		Priority:         p.Priority,
		RequestedMode:    mode,
		RequestedDevice:  device,
		EngineHint:       engineHint,
		RoutingReason:    reason,
		IdempotencyKey:   jobs.NormalizeIdempotencyKey(p.IdempotencyKey),
		CreatedAt:        now,
		MaxLifetimeAt:    now.Add(s.Cfg.MaxJobLifetime),
		MaxAttempts:      s.Cfg.MaxAttempts,
		UpdatedAt:        now,
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO jobs (`+jobCols+`) VALUES (`+placeholders(35)+`)`,
		job.ID, job.OwnerID, job.InputName, job.MimeType, job.Format, job.InputSHA256, job.InputSize,
		job.PageCount, string(job.ProcessingStatus), string(job.RetentionStatus), job.Priority,
		job.RequestedMode, job.RequestedDevice, job.EngineHint, job.ActualEngine, job.RoutingReason,
		nullIfEmpty(job.IdempotencyKey), ts(job.CreatedAt), nil, nil, nil, ts(job.MaxLifetimeAt), 0, job.MaxAttempts,
		nil, nil, nil, nil, 0, 0, "", ttl, ts(now), nil, ts(now))
	if err != nil {
		_ = s.Store.DeleteJob(jobID)
		if isUniqueViolation(err) && job.IdempotencyKey != "" {
			if existing, ferr := s.findByIdemKey(ctx, p.OwnerID, job.IdempotencyKey); ferr == nil && existing != nil {
				return existing, false, nil
			}
		}
		return nil, false, NewError(500, jobs.ErrInternal, "insert job: "+err.Error())
	}
	s.appendEvent(ctx, jobID, "created", map[string]any{
		"status": job.ProcessingStatus, "format": job.Format, "engine_hint": job.EngineHint,
	})
	return job, true, nil
}

func (s *Service) validateStaged(jobID string, w *storage.InputWriter, name string, lim storage.Limits) error {
	tmp := filepath.Join(s.Store.Root, "tmp", jobID+".input.part")
	return validateFile(tmp, name, lim)
}

func sniffCommitted(path, name string, lim storage.Limits) (storage.Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return storage.Format{}, NewError(500, jobs.ErrInternal, err.Error())
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	format, err := storage.SniffFormat(name, head[:n], lim)
	if err != nil {
		return storage.Format{}, validationErr(err)
	}
	return format, nil
}

func validateFile(path, name string, lim storage.Limits) error {
	f, err := os.Open(path)
	if err != nil {
		return NewError(500, jobs.ErrInternal, err.Error())
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	if _, err := storage.SniffFormat(name, head[:n], lim); err != nil {
		return validationErr(err)
	}
	return nil
}

func validationErr(err error) error {
	var ve *storage.ValidationError
	if errors.As(err, &ve) {
		status := 422
		switch ve.Code {
		case jobs.ErrTooLarge, jobs.ErrExpandedTooLarge:
			status = 413
		case jobs.ErrUnsupportedType:
			status = 415
		case jobs.ErrMalformed:
			status = 422
		}
		return NewError(status, ve.Code, ve.Msg)
	}
	return NewError(422, jobs.ErrMalformed, err.Error())
}

// textFormats are handled by the native engine in every mode. ML engines are
// slower by orders of magnitude and get encodings (GB18030, UTF-16) wrong, so
// mode=accurate must not route them away from native.
func isTextFormat(format string) bool {
	switch format {
	case "txt", "md", "csv", "tsv":
		return true
	}
	return false
}

// route chooses the engine hint and records why. It never silently degrades a
// gpu_required request.
func (s *Service) route(ctx context.Context, format, mode, device string) (string, string, error) {
	// F02: text formats are always served by native when it is configured.
	// gpu_required is exempt on purpose: an explicit GPU request must never be
	// satisfied by a CPU engine.
	if isTextFormat(format) && device != jobs.DeviceGPURequired {
		for _, e := range s.Cfg.Engines {
			if e.Name == "native" && e.Supports(format, jobs.ModeAuto) {
				return e.Name, fmt.Sprintf(
					"format=%s mode=%s device=%s -> native (%s); text formats always use the native engine (encoding-correct, no model load)",
					format, mode, device, e.Version), nil
			}
		}
	}
	cands := make([]EngineSpec, 0, len(s.Cfg.Engines))
	for _, e := range s.Cfg.Engines {
		switch device {
		case jobs.DeviceCPU:
			if !e.CPU() {
				continue
			}
		case jobs.DeviceGPURequired:
			if !e.GPU() {
				continue
			}
		case jobs.DeviceGPUPreferred:
			// keep both, GPU sorted first
		case jobs.DeviceAuto:
			// keep both
		}
		if !e.Supports(format, mode) {
			continue
		}
		cands = append(cands, e)
	}
	if len(cands) == 0 {
		if device == jobs.DeviceGPURequired {
			return "", "", NewError(409, jobs.ErrGPUUnavailable,
				fmt.Sprintf("no GPU engine can handle format %q; refusing to downgrade", format))
		}
		return "", "", NewError(415, jobs.ErrUnsupportedType,
			fmt.Sprintf("no engine available for format %q in mode %q", format, mode))
	}
	// Device preference is explicit:
	//   auto         - no device preference, order by priority (a GPU host must
	//                  not silently change routing for callers who did not ask)
	//   gpu_preferred- GPU engines first, then priority
	//   gpu_required - GPU engines only (filtered above)
	//   cpu          - CPU engines only (filtered above)
	sort.SliceStable(cands, func(i, j int) bool {
		if device == jobs.DeviceGPUPreferred {
			gi, gj := cands[i].GPU(), cands[j].GPU()
			if gi != gj {
				return gi
			}
		}
		if cands[i].Priority != cands[j].Priority {
			return cands[i].Priority > cands[j].Priority
		}
		return cands[i].Name < cands[j].Name
	})
	chosen := cands[0]

	// gpu_required needs a live GPU worker; otherwise the job would wait forever.
	if device == jobs.DeviceGPURequired {
		n, err := s.countGPUWorkers(ctx, chosen.Name)
		if err == nil && n == 0 {
			return "", "", NewError(409, jobs.ErrGPUUnavailable,
				"no GPU worker is currently registered; retry later or set device=gpu_preferred")
		}
	}
	reason := fmt.Sprintf("format=%s mode=%s device=%s -> %s/%s (%s)", format, mode, device,
		chosen.Name, chosen.Version, chosen.Device)
	if device == jobs.DeviceGPUPreferred && !chosen.GPU() {
		reason += "; fallback=cpu (no GPU-capable engine matched)"
	}
	return chosen.Name, reason, nil
}

func (s *Service) limits() storage.Limits {
	if l, ok := s.Cfg.Limits.(storage.Limits); ok && l.MaxBytes > 0 {
		return l
	}
	return storage.DefaultLimits()
}

func (s *Service) countGPUWorkers(ctx context.Context, engine string) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT engines FROM workers WHERE device_type IN ('gpu','cuda','mps') AND heartbeat_at > ?`,
		ts(nowUTC().Add(-3*time.Minute)))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return n, err
		}
		var names []string
		_ = json.Unmarshal([]byte(raw), &names)
		for _, nm := range names {
			if nm == engine || engine == "" {
				n++
				break
			}
		}
	}
	return n, rows.Err()
}

func (s *Service) findByIdemKey(ctx context.Context, owner, key string) (*jobs.Job, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE owner_id = ? AND idempotency_key = ?`, owner, key)
	return scanJob(row)
}

// ---------------------------------------------------------------- queries

// GetJob returns a job only to its owner. Unknown and foreign job IDs are
// indistinguishable to the caller (no cross-tenant existence leak).
func (s *Service) GetJob(ctx context.Context, jobID, ownerID string) (*jobs.Job, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, jobID)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NewError(404, jobs.ErrNotFound, "job not found")
	}
	if err != nil {
		return nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	if ownerID != "" && job.OwnerID != ownerID {
		return nil, NewError(404, jobs.ErrNotFound, "job not found")
	}
	return job, nil
}

// GetManifest returns the committed result manifest.
func (s *Service) GetManifest(jobID string) (*storage.Manifest, error) {
	return s.Store.ReadManifest(jobID)
}

// OpenResult opens one artifact for reading.
func (s *Service) OpenResult(jobID, format string) (*os.File, os.FileInfo, *storage.Manifest, error) {
	m, err := s.Store.ReadManifest(jobID)
	if err != nil {
		return nil, nil, nil, NewError(410, jobs.ErrGone, "result is no longer available")
	}
	name := artifactName(format)
	p := s.Store.ResultPath(jobID, name)
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, nil, NewError(410, jobs.ErrGone, "result artifact missing")
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	return f, st, m, nil
}

func artifactName(format string) string {
	switch format {
	case "text":
		return "result.txt"
	case "json":
		return "result.json"
	default:
		return "result.md"
	}
}

// ---------------------------------------------------------------- claiming

// ClaimedTask is what a worker receives.
type ClaimedTask struct {
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

// WorkerCaps is the capability set used to filter claims.
type WorkerCaps struct {
	WorkerID string   `json:"worker_id"`
	Engines  []string `json:"engines"`
	Device   string   `json:"device_type"`
	CanGPU   bool     `json:"-"`
}

// Claim atomically leases one queued job matching the worker's capabilities.
// Returns (nil, nil) when the queue has nothing suitable.
func (s *Service) Claim(ctx context.Context, caps WorkerCaps) (*ClaimedTask, error) {
	engines := caps.Engines
	if len(engines) == 0 {
		engines = []string{"auto"}
	}
	in := make([]string, 0, len(engines)+1)
	engineArgs := []any{}
	for _, e := range engines {
		in = append(in, "?")
		engineArgs = append(engineArgs, e)
	}
	in = append(in, "?")
	engineArgs = append(engineArgs, "auto")
	now := nowUTC()
	deviceFilter := ""
	if caps.Device != "gpu" && caps.Device != "cuda" && caps.Device != "mps" {
		deviceFilter = "AND requested_device != 'gpu_required'"
	}
	q := `UPDATE jobs SET processing_status = 'running', started_at = COALESCE(started_at, ?),
	        current_attempt = current_attempt + 1, progress = 0, progress_stage = 'claimed', updated_at = ?
	      WHERE id = (
	        SELECT id FROM jobs
	        WHERE processing_status = 'queued' AND (available_at = '' OR available_at <= ?)
	          AND (engine_hint IN (` + strings.Join(in, ",") + `) OR engine_hint = '')
	          AND max_lifetime_at > ? ` + deviceFilter + `
	        ORDER BY priority DESC, created_at ASC LIMIT 1)
	        AND processing_status = 'queued'
	      RETURNING id, format, mime_type, input_name, input_sha256, input_size, page_count,
	                requested_mode, requested_device, engine_hint, routing_reason, current_attempt, max_lifetime_at`

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	defer func() { _ = tx.Rollback() }()

	// Parameter order: started_at, updated_at, available_at, engine list, max_lifetime_at.
	args := append([]any{ts(now), ts(now), ts(now)}, engineArgs...)
	args = append(args, ts(now))
	row := tx.QueryRowContext(ctx, q, args...)
	var t ClaimedTask
	var maxLife string
	if err := row.Scan(&t.JobID, &t.Format, &t.MimeType, &t.InputName, &t.InputSHA256, &t.InputSize,
		&t.PageCount, &t.Mode, &t.Device, &t.EngineHint, &t.RoutingReason, &t.AttemptNo, &maxLife); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, NewError(500, jobs.ErrInternal, "claim: "+err.Error())
	}
	if t.MaxLifetimeAt, err = parseTS(maxLife); err != nil {
		return nil, NewError(500, jobs.ErrInternal, "claim: bad max_lifetime_at")
	}
	t.LeaseToken = jobs.NewToken()
	t.LeaseUntil = now.Add(s.Cfg.WorkerLeaseTTL)
	if _, err := tx.ExecContext(ctx, `INSERT INTO attempts
		(job_id, attempt_no, worker_id, lease_token, lease_until, started_at, state, device_type)
		VALUES (?,?,?,?,?,?,?,?)`,
		t.JobID, t.AttemptNo, caps.WorkerID, t.LeaseToken, ts(t.LeaseUntil), ts(now), jobs.AttemptRunning, caps.Device); err != nil {
		return nil, NewError(500, jobs.ErrInternal, "claim: insert attempt: "+err.Error())
	}
	if err := tx.Commit(); err != nil {
		return nil, NewError(500, jobs.ErrInternal, "claim: commit: "+err.Error())
	}
	s.appendEvent(ctx, t.JobID, "running", map[string]any{
		"attempt": t.AttemptNo, "worker_id": caps.WorkerID, "lease_until": t.LeaseUntil,
	})
	return &t, nil
}

// Heartbeat renews a lease and records throttled progress.
func (s *Service) Heartbeat(ctx context.Context, jobID, token string, progress int, stage string) (time.Time, error) {
	now := nowUTC()
	until := now.Add(s.Cfg.WorkerLeaseTTL)

	// Look the attempt up by token first: a cancelled attempt must be reported
	// as "cancelled" (actionable) instead of "lease lost", which is what made
	// the worker's cancel path unreachable.
	var state, leaseUntil string
	err := s.DB.QueryRowContext(ctx, `SELECT state, lease_until FROM attempts
		WHERE job_id = ? AND lease_token = ?`, jobID, token).Scan(&state, &leaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, NewError(409, jobs.ErrLeaseLost, "unknown lease token")
	}
	if err != nil {
		return time.Time{}, NewError(500, jobs.ErrInternal, err.Error())
	}
	if state == jobs.AttemptCancelled {
		return time.Time{}, NewError(409, jobs.ErrCancelled, "job was cancelled")
	}
	if state != jobs.AttemptRunning {
		return time.Time{}, NewError(409, jobs.ErrLeaseLost, "attempt is no longer running")
	}
	if u, perr := parseTS(leaseUntil); perr != nil || u.Before(now) {
		return time.Time{}, NewError(409, jobs.ErrLeaseLost, "lease expired")
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE attempts SET lease_until = ?, last_heartbeat_at = ?
		WHERE job_id = ? AND lease_token = ? AND state = 'running'`,
		ts(until), ts(now), jobID, token)
	if err != nil {
		return time.Time{}, NewError(500, jobs.ErrInternal, err.Error())
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return time.Time{}, NewError(409, jobs.ErrLeaseLost, "lease is not valid (expired or superseded)")
	}
	if progress >= 0 {
		if progress > 100 {
			progress = 100
		}
		// Progress updates are cheap but still throttled by the worker side;
		// this clamp keeps SQLite writes bounded.
		_, err = s.DB.ExecContext(ctx, `UPDATE jobs SET progress = ?, progress_stage = ?, updated_at = ?
			WHERE id = ? AND processing_status = 'running' AND progress <= ?`,
			progress, stage, ts(now), jobID, progress)
		if err != nil {
			return time.Time{}, NewError(500, jobs.ErrInternal, err.Error())
		}
	}
	var st string
	if err := s.DB.QueryRowContext(ctx, `SELECT processing_status FROM jobs WHERE id = ?`, jobID).Scan(&st); err != nil {
		return time.Time{}, NewError(500, jobs.ErrInternal, err.Error())
	}
	if jobs.ProcessingStatus(st) == jobs.StatusCancelled {
		return time.Time{}, NewError(409, jobs.ErrCancelled, "job was cancelled")
	}
	return until, nil
}

// CompleteParams carries a worker's successful result.
type CompleteParams struct {
	JobID     string
	Token     string
	Engine    string
	Version   string
	Device    string
	Doc       *documentir.Document
	Markdown  []byte
	Text      []byte
	JSON      []byte
	PageCount int
	Warnings  int
}

// Complete publishes artifacts then commits the job with a fencing check.
//
// File I/O happens before the transaction on purpose: the DB must not be held
// while writing. If the lease was lost, the freshly published files are
// removed so a superseded attempt cannot leave a result behind.
func (s *Service) Complete(ctx context.Context, p CompleteParams) (*jobs.Job, error) {
	if err := s.checkLease(ctx, p.JobID, p.Token); err != nil {
		return nil, err
	}
	manifest, err := s.Store.CommitResult(p.JobID, p.Engine, p.Version, map[string]io.Reader{
		"result.md":   bytesReader(p.Markdown),
		"result.txt":  bytesReader(p.Text),
		"result.json": bytesReader(p.JSON),
	})
	if err != nil {
		return nil, NewError(500, jobs.ErrInternal, "commit result: "+err.Error())
	}

	now := nowUTC()
	ttl := s.Cfg.ResultTTL
	var reqTTL int
	_ = s.DB.QueryRowContext(ctx, `SELECT requested_ttl_seconds FROM jobs WHERE id = ?`, p.JobID).Scan(&reqTTL)
	if reqTTL > 0 {
		ttl = time.Duration(reqTTL) * time.Second
	}
	if ttl > s.Cfg.MaxResultTTL {
		ttl = s.Cfg.MaxResultTTL
	}
	expires := now.Add(ttl)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		_ = s.Store.DeleteJob(p.JobID)
		return nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `UPDATE jobs SET
			processing_status = 'completed', completed_at = ?, result_expires_at = ?,
			result_version = ?, result_manifest_path = ?, page_count = CASE WHEN ? > 0 THEN ? ELSE page_count END,
			actual_engine = ?, warning_count = ?, progress = 100, progress_stage = 'completed',
			error_code = NULL, error_message = NULL, updated_at = ?
		WHERE id = ? AND processing_status = 'running'
		  AND current_attempt = (SELECT attempt_no FROM attempts WHERE job_id = ? AND lease_token = ? AND state = 'running')
		  AND EXISTS (SELECT 1 FROM attempts WHERE job_id = ? AND lease_token = ? AND state = 'running' AND lease_until > ?)`,
		ts(now), ts(expires), manifest.Version, filepath.Join(s.Store.ResultDir(p.JobID), "manifest.json"),
		p.PageCount, p.PageCount, p.Engine, p.Warnings, ts(now), p.JobID, p.JobID, p.Token, p.JobID, p.Token, ts(now))
	if err != nil {
		_ = s.Store.DeleteJob(p.JobID)
		return nil, NewError(500, jobs.ErrInternal, "complete: "+err.Error())
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		_ = s.Store.DeleteJob(p.JobID) // late result from a superseded attempt
		return nil, NewError(409, jobs.ErrLeaseLost, "lease expired or was superseded; result rejected")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attempts SET state = 'succeeded', engine = ?, device_type = ?
		WHERE job_id = ? AND lease_token = ?`, p.Engine, p.Device, p.JobID, p.Token); err != nil {
		_ = s.Store.DeleteJob(p.JobID)
		return nil, NewError(500, jobs.ErrInternal, "complete: attempt: "+err.Error())
	}
	for _, a := range manifest.Files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(job_id, kind, path, size, sha256, generation, ready_at)
			VALUES(?,?,?,?,?,1,?) ON CONFLICT(job_id, kind) DO UPDATE SET path=excluded.path, size=excluded.size,
			sha256=excluded.sha256, generation=artifacts.generation+1, ready_at=excluded.ready_at`,
			p.JobID, a.Name, s.Store.ResultPath(p.JobID, a.Name), a.Size, a.SHA256, ts(now)); err != nil {
			_ = s.Store.DeleteJob(p.JobID)
			return nil, NewError(500, jobs.ErrInternal, "complete: artifact: "+err.Error())
		}
	}
	if err := tx.Commit(); err != nil {
		_ = s.Store.DeleteJob(p.JobID)
		return nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	s.appendEvent(ctx, p.JobID, "completed", map[string]any{
		"result_version": manifest.Version, "engine": p.Engine, "engine_version": p.Version,
		"result_expires_at": expires, "pages": p.PageCount, "warnings": p.Warnings,
	})
	return s.GetJob(ctx, p.JobID, "")
}

// Fail records a classified failure, retrying transient ones with backoff.
func (s *Service) Fail(ctx context.Context, jobID, token, code, msg string, retryable bool) (*jobs.Job, error) {
	// A cancelled attempt cannot pass the lease check, but the worker still has
	// to finish cleanly. Treat it as an idempotent no-op: the job is already
	// cancelled and no further state change is allowed.
	var state string
	err := s.DB.QueryRowContext(ctx, `SELECT state FROM attempts WHERE job_id = ? AND lease_token = ?`,
		jobID, token).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NewError(409, jobs.ErrLeaseLost, "unknown lease token")
	}
	if err != nil {
		return nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	if state == jobs.AttemptCancelled {
		return s.GetJob(ctx, jobID, "")
	}
	if err := s.checkLease(ctx, jobID, token); err != nil {
		return nil, err
	}
	now := nowUTC()
	var attempt, maxAttempts int
	if err := s.DB.QueryRowContext(ctx, `SELECT current_attempt, max_attempts FROM jobs WHERE id = ?`, jobID).
		Scan(&attempt, &maxAttempts); err != nil {
		return nil, NewError(404, jobs.ErrNotFound, "job not found")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	defer func() { _ = tx.Rollback() }()

	if retryable && attempt < maxAttempts {
		backoff := time.Duration(1<<minInt(attempt, 6)) * time.Second
		if backoff > time.Minute {
			backoff = time.Minute
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET processing_status = 'queued', progress = 0,
			progress_stage = 'retry_wait', error_code = ?, error_message = ?, available_at = ?, updated_at = ?
			WHERE id = ? AND processing_status = 'running'`, code, truncate(msg, 500), ts(now.Add(backoff)), ts(now), jobID); err != nil {
			return nil, NewError(500, jobs.ErrInternal, err.Error())
		}
		if _, err := tx.ExecContext(ctx, `UPDATE attempts SET state = 'failed', error = ? WHERE job_id = ? AND lease_token = ?`,
			truncate(msg, 500), jobID, token); err != nil {
			return nil, NewError(500, jobs.ErrInternal, err.Error())
		}
		evt := "retry_scheduled"
		if err := tx.Commit(); err != nil {
			return nil, NewError(500, jobs.ErrInternal, err.Error())
		}
		s.appendEvent(ctx, jobID, evt, map[string]any{"code": code, "attempt": attempt, "backoff_s": backoff.Seconds()})
		return s.GetJob(ctx, jobID, "")
	}

	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET processing_status = 'failed', completed_at = ?,
		error_code = ?, error_message = ?, retention_status = CASE WHEN retention_status = 'active'
		THEN 'active' ELSE retention_status END, updated_at = ?
		WHERE id = ? AND processing_status = 'running'`,
		ts(now), code, truncate(msg, 500), ts(now), jobID); err != nil {
		return nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attempts SET state = 'failed', error = ? WHERE job_id = ? AND lease_token = ?`,
		truncate(msg, 500), jobID, token); err != nil {
		return nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	if err := tx.Commit(); err != nil {
		return nil, NewError(500, jobs.ErrInternal, err.Error())
	}
	s.appendEvent(ctx, jobID, "failed", map[string]any{"code": code, "message": truncate(msg, 500), "retryable": retryable})
	_ = s.Store.DeleteJob(jobID) // failed jobs keep metadata only; no input retained
	return s.GetJob(ctx, jobID, "")
}

func (s *Service) checkLease(ctx context.Context, jobID, token string) error {
	var state string
	var until string
	err := s.DB.QueryRowContext(ctx, `SELECT state, lease_until FROM attempts WHERE job_id = ? AND lease_token = ?`,
		jobID, token).Scan(&state, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return NewError(409, jobs.ErrLeaseLost, "unknown lease token")
	}
	if err != nil {
		return NewError(500, jobs.ErrInternal, err.Error())
	}
	u, perr := parseTS(until)
	if perr != nil || state != jobs.AttemptRunning || u.Before(nowUTC()) {
		return NewError(409, jobs.ErrLeaseLost, "lease expired or superseded; result rejected")
	}
	return nil
}

// ---------------------------------------------------------------- ACK / cancel

// Ack accepts the client's intent to release a result. It is idempotent and
// transitions active -> release_pending without deleting anything.
func (s *Service) Ack(ctx context.Context, jobID, ownerID, resultVersion, action string) (*jobs.Job, error) {
	job, err := s.GetJob(ctx, jobID, ownerID)
	if err != nil {
		return nil, err
	}
	if action == "" {
		action = "release"
	}
	if action != "release" {
		return nil, NewError(400, jobs.ErrInvalidRequest, "unsupported ack action (only \"release\" in Phase 0)")
	}
	now := nowUTC()
	if job.RetentionStatus == jobs.RetentionActive {
		if resultVersion == "" {
			return nil, NewError(400, jobs.ErrInvalidRequest, "result_version is required to ACK a result")
		}
		if job.ResultVersion != "" && resultVersion != job.ResultVersion {
			return nil, NewError(409, jobs.ErrConflict, "result_version does not match the current result")
		}
		res, err := s.DB.ExecContext(ctx, `UPDATE jobs SET retention_status = 'release_pending', updated_at = ?
			WHERE id = ? AND owner_id = ? AND retention_status = 'active'`, ts(now), jobID, job.OwnerID)
		if err != nil {
			return nil, NewError(500, jobs.ErrInternal, err.Error())
		}
		if n, _ := res.RowsAffected(); n > 0 {
			s.appendEvent(ctx, jobID, "release_pending", map[string]any{"reason": "ack"})
		}
		return s.GetJob(ctx, jobID, ownerID)
	}
	// Already releasing/released: idempotent success.
	return job, nil
}

// Cancel stops a queued/running job.
//
// A *completed* result is not cancelled: it is released, and releasing storage
// requires the same proof as ACK (`result_version`), otherwise cancel would be a
// way to bypass the ACK guard. Repeat calls are idempotent.
func (s *Service) Cancel(ctx context.Context, jobID, ownerID, resultVersion string) (*jobs.Job, error) {
	job, err := s.GetJob(ctx, jobID, ownerID)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	switch job.ProcessingStatus {
	case jobs.StatusQueued, jobs.StatusRunning:
		if _, err := s.DB.ExecContext(ctx, `UPDATE jobs SET processing_status = 'cancelled', completed_at = ?,
			retention_status = 'release_pending', progress_stage = 'cancelled', updated_at = ?
			WHERE id = ? AND processing_status IN ('queued','running')`, ts(now), ts(now), jobID); err != nil {
			return nil, NewError(500, jobs.ErrInternal, err.Error())
		}
		_, _ = s.DB.ExecContext(ctx, `UPDATE attempts SET state = 'cancelled' WHERE job_id = ? AND state = 'running'`, jobID)
		s.appendEvent(ctx, jobID, "cancelled", map[string]any{"reason": "client_request"})
	case jobs.StatusCompleted:
		if job.RetentionStatus == jobs.RetentionActive {
			if resultVersion == "" {
				return nil, NewError(409, jobs.ErrConflict,
					"job already completed: cancel would release the result and requires result_version (or call POST /ack with the manifest version)")
			}
			if job.ResultVersion != "" && resultVersion != job.ResultVersion {
				return nil, NewError(409, jobs.ErrConflict, "result_version does not match the current result")
			}
			res, err := s.DB.ExecContext(ctx, `UPDATE jobs SET retention_status = 'release_pending', updated_at = ?
				WHERE id = ? AND owner_id = ? AND retention_status = 'active'`, ts(now), jobID, job.OwnerID)
			if err != nil {
				return nil, NewError(500, jobs.ErrInternal, err.Error())
			}
			if n, _ := res.RowsAffected(); n > 0 {
				s.appendEvent(ctx, jobID, "release_pending", map[string]any{"reason": "cancel_after_complete"})
			}
		}
	}
	return s.GetJob(ctx, jobID, ownerID)
}

// ---------------------------------------------------------------- download leases

// BeginDownload creates a usage lease, refusing when retention has begun.
func (s *Service) BeginDownload(ctx context.Context, jobID, ownerID string) (*jobs.Job, string, time.Time, error) {
	job, err := s.GetJob(ctx, jobID, ownerID)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if !job.ResultAvailable() || job.Gone() {
		code := jobs.ErrGone
		status := 410
		if job.ProcessingStatus != jobs.StatusCompleted {
			return nil, "", time.Time{}, NewError(409, jobs.ErrConflict,
				fmt.Sprintf("result not available yet (status=%s)", job.ProcessingStatus))
		}
		return nil, "", time.Time{}, NewError(status, code, "result released or expired")
	}
	now := nowUTC()
	exp := now.Add(s.Cfg.DownloadLeaseTTL)
	leaseID := jobs.NewLeaseID()
	// One statement: the lease only exists if the job is still downloadable at
	// this instant. A separate check-then-insert let the cleaner flip the job to
	// 'deleting' in between and produce a lease for a vanished artifact.
	res, err := s.DB.ExecContext(ctx, `INSERT INTO download_leases
			(lease_id, job_id, created_at, expires_at, last_renewed_at)
		SELECT ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM jobs WHERE id = ? AND processing_status = 'completed'
		              AND retention_status = 'active' AND result_version IS NOT NULL)`,
		leaseID, jobID, ts(now), ts(exp), ts(now), jobID)
	if err != nil {
		return nil, "", time.Time{}, NewError(500, jobs.ErrInternal, err.Error())
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, "", time.Time{}, NewError(410, jobs.ErrGone, "result released or expired")
	}
	return job, leaseID, exp, nil
}

// RenewDownload extends a lease while the response body is still streaming.
func (s *Service) RenewDownload(ctx context.Context, leaseID string) (time.Time, error) {
	now := nowUTC()
	exp := now.Add(s.Cfg.DownloadLeaseTTL)
	res, err := s.DB.ExecContext(ctx, `UPDATE download_leases SET expires_at = ?, last_renewed_at = ? WHERE lease_id = ?`,
		ts(exp), ts(now), leaseID)
	if err != nil {
		return time.Time{}, NewError(500, jobs.ErrInternal, err.Error())
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return time.Time{}, NewError(404, jobs.ErrNotFound, "download lease not found")
	}
	return exp, nil
}

// EndDownload drops a lease (called after the body was fully written).
func (s *Service) EndDownload(ctx context.Context, leaseID string) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM download_leases WHERE lease_id = ?`, leaseID)
}

// ---------------------------------------------------------------- workers

// RegisterWorker upserts a worker's capabilities and heartbeat.
func (s *Service) RegisterWorker(ctx context.Context, w WorkerRegistration) error {
	now := nowUTC()
	eng, _ := json.Marshal(w.Engines)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO workers
		(worker_id, host, engines, device_type, gpu_model, gpu_memory_bytes, cpu_cores, memory_bytes,
		 max_concurrency, registered_at, heartbeat_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(worker_id) DO UPDATE SET host=excluded.host, engines=excluded.engines,
		 device_type=excluded.device_type, gpu_model=excluded.gpu_model,
		 gpu_memory_bytes=excluded.gpu_memory_bytes, cpu_cores=excluded.cpu_cores,
		 memory_bytes=excluded.memory_bytes, max_concurrency=excluded.max_concurrency,
		 heartbeat_at=excluded.heartbeat_at`,
		w.WorkerID, w.Host, string(eng), w.DeviceType, w.GPUModel, w.GPUMemoryBytes, w.CPUCores,
		w.MemoryBytes, w.MaxConcurrency, ts(now), ts(now))
	if err != nil {
		return NewError(500, jobs.ErrInternal, err.Error())
	}
	return nil
}

// WorkerRegistration is the register payload.
type WorkerRegistration struct {
	WorkerID       string   `json:"worker_id"`
	Host           string   `json:"host"`
	Engines        []string `json:"engines"`
	DeviceType     string   `json:"device_type"`
	GPUModel       string   `json:"gpu_model"`
	GPUMemoryBytes int64    `json:"gpu_memory_bytes"`
	CPUCores       int      `json:"cpu_cores"`
	MemoryBytes    int64    `json:"memory_bytes"`
	MaxConcurrency int      `json:"max_concurrency"`
}

// Workers lists known workers (diagnostics).
func (s *Service) Workers(ctx context.Context) ([]WorkerRegistration, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT worker_id, host, engines, device_type, gpu_model,
		gpu_memory_bytes, cpu_cores, memory_bytes, max_concurrency FROM workers ORDER BY registered_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkerRegistration{}
	for rows.Next() {
		var w WorkerRegistration
		var eng string
		if err := rows.Scan(&w.WorkerID, &w.Host, &eng, &w.DeviceType, &w.GPUModel, &w.GPUMemoryBytes,
			&w.CPUCores, &w.MemoryBytes, &w.MaxConcurrency); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(eng), &w.Engines)
		out = append(out, w)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- events

// Event is one job lifecycle event (SSE compatible).
type Event struct {
	ID        int64           `json:"event_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *Service) appendEvent(ctx context.Context, jobID, typ string, payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO job_events(job_id, type, payload, created_at) VALUES(?,?,?,?)`,
		jobID, typ, string(b), ts(nowUTC()))
}

// OldestEventID reports the oldest retained event for a job, so a client cursor
// older than that can be told its replay window is gone (F07). Zero means no
// events are retained (job purged or never had events).
func (s *Service) OldestEventID(ctx context.Context, jobID string) (int64, error) {
	var id sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT MIN(event_id) FROM job_events WHERE job_id = ?`, jobID).
		Scan(&id); err != nil {
		return 0, err
	}
	return id.Int64, nil
}

// Events returns events with id > afterID for the job (owner-checked upstream).
func (s *Service) Events(ctx context.Context, jobID string, afterID int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT event_id, type, payload, created_at FROM job_events
		WHERE job_id = ? AND event_id > ? ORDER BY event_id LIMIT ?`, jobID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var payload, created string
		if err := rows.Scan(&e.ID, &e.Type, &payload, &created); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		if t, err := parseTS(created); err == nil {
			e.CreatedAt = t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- scanning helpers

type rowScanner interface{ Scan(dest ...any) error }

func scanJob(row rowScanner) (*jobs.Job, error) {
	var j jobs.Job
	var proc, ret string
	var idem, actualEngine, routing, createdAt, startedAt, completedAt, expiresAt, maxLife,
		resultVersion, manifestPath, errCode, errMsg, progressStage, availableAt, tombstone, updatedAt sql.NullString
	var pageCount, attempt, maxAttempts, warnings, progress, reqTTL sql.NullInt64
	var priority sql.NullInt64
	if err := row.Scan(&j.ID, &j.OwnerID, &j.InputName, &j.MimeType, &j.Format, &j.InputSHA256, &j.InputSize,
		&pageCount, &proc, &ret, &priority, &j.RequestedMode, &j.RequestedDevice, &j.EngineHint,
		&actualEngine, &routing, &idem, &createdAt, &startedAt, &completedAt, &expiresAt, &maxLife,
		&attempt, &maxAttempts, &resultVersion, &manifestPath, &errCode, &errMsg, &warnings, &progress,
		&progressStage, &reqTTL, &availableAt, &tombstone, &updatedAt); err != nil {
		return nil, err
	}
	j.ProcessingStatus = jobs.ProcessingStatus(proc)
	j.RetentionStatus = jobs.RetentionStatus(ret)
	j.PageCount = int(pageCount.Int64)
	j.Priority = int(priority.Int64)
	j.ActualEngine = actualEngine.String
	j.RoutingReason = routing.String
	j.IdempotencyKey = idem.String
	j.CurrentAttempt = int(attempt.Int64)
	j.MaxAttempts = int(maxAttempts.Int64)
	j.ResultVersion = resultVersion.String
	j.ResultManifestPath = manifestPath.String
	j.ErrorCode = errCode.String
	j.ErrorMessage = errMsg.String
	j.WarningCount = int(warnings.Int64)
	j.Progress = int(progress.Int64)
	j.ProgressStage = progressStage.String
	if t, err := parseTS(createdAt.String); err == nil {
		j.CreatedAt = t
	}
	if t, err := parseTS(maxLife.String); err == nil {
		j.MaxLifetimeAt = t
	}
	if t, err := parseTS(updatedAt.String); err == nil {
		j.UpdatedAt = t
	}
	j.StartedAt = parseTSPtr(startedAt)
	j.CompletedAt = parseTSPtr(completedAt)
	j.ResultExpiresAt = parseTSPtr(expiresAt)
	j.TombstoneExpiresAt = parseTSPtr(tombstone)
	return &j, nil
}

func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func sanitizeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "/" || name == "" {
		return "upload"
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > 180 {
		out = out[:180]
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// nullIfEmpty stores NULL for empty optional strings so partial unique indexes
// (owner_id, idempotency_key) do not collide on "".
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

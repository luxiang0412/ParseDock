package coordinator

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
	"parsedock.dev/parsedock/internal/storage"
)

// Server is the Coordinator's internal HTTP surface. Two muxes are exposed:
//
//	HandlePublic   /v1/*        job API consumed by API replicas (internal token
//	                            + X-Parsedock-Owner injected after auth)
//	HandleInternal /internal/*  worker protocol (internal token + lease token)
//
// Neither mux must ever be exposed to the public internet directly.
type Server struct {
	Svc           *Service
	InternalToken string
	Log           *slog.Logger
}

// NewServer builds the coordinator HTTP server.
func NewServer(svc *Service, internalToken string, log *slog.Logger) *Server {
	return &Server{Svc: svc, InternalToken: internalToken, Log: log}
}

// ---------------------------------------------------------------- public mux

// PublicMux serves /v1 and probes.
func (s *Server) PublicMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /v1/jobs", s.guard(s.handleCreateJob))
	m.HandleFunc("GET /v1/jobs/{id}", s.guard(s.handleGetJob))
	m.HandleFunc("GET /v1/jobs/{id}/events", s.guard(s.handleEvents))
	m.HandleFunc("GET /v1/jobs/{id}/result", s.guard(s.handleResult))
	m.HandleFunc("POST /v1/jobs/{id}/ack", s.guard(s.handleAck))
	m.HandleFunc("POST /v1/jobs/{id}/cancel", s.guard(s.handleCancel))
	m.HandleFunc("POST /v1/parse", s.guard(s.handleSyncParse))
	m.HandleFunc("GET /healthz", s.handleHealthz)
	m.HandleFunc("GET /readyz", s.handleReadyz)
	m.HandleFunc("GET /internal/diagnostics/workers", s.guard(s.handleWorkers))
	return m
}

func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.InternalToken != "" {
			got := r.Header.Get("X-Internal-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.InternalToken)) != 1 {
				writeErr(w, NewError(401, jobs.ErrUnauthorized, "invalid internal token"))
				return
			}
		}
		owner := r.Header.Get("X-Parsedock-Owner")
		if owner == "" {
			writeErr(w, NewError(401, jobs.ErrUnauthorized, "missing owner identity"))
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ownerKey{}, owner)))
	}
}

type ownerKey struct{}

func owner(r *http.Request) string {
	v, _ := r.Context().Value(ownerKey{}).(string)
	return v
}

// ---------------------------------------------------------------- handlers

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mode := firstNonEmpty(q.Get("mode"), jobs.ModeAuto)
	device := firstNonEmpty(q.Get("device"), jobs.DeviceAuto)
	priority, _ := strconv.Atoi(q.Get("priority"))
	ttlSeconds, _ := strconv.Atoi(q.Get("ttl_seconds"))
	idemKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), q.Get("idempotency_key"))

	maxBody := s.Svc.Cfg.MaxUploadBytes + (8 << 20) // multipart overhead
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, NewError(400, jobs.ErrInvalidRequest, "expected multipart/form-data body"))
		return
	}
	form := map[string]string{}
	var created *jobs.Job
	var isNew bool
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeErr(w, NewError(413, jobs.ErrTooLarge, fmt.Sprintf("upload exceeds %d bytes", s.Svc.Cfg.MaxUploadBytes)))
				return
			}
			writeErr(w, NewError(400, jobs.ErrMalformed, "malformed multipart body: "+err.Error()))
			return
		}
		name := part.FormName()
		if part.FileName() == "" && name != "file" {
			b, err := io.ReadAll(io.LimitReader(part, 64<<10))
			_ = part.Close()
			if err != nil {
				writeErr(w, NewError(400, jobs.ErrMalformed, "read form field"))
				return
			}
			form[name] = strings.TrimSpace(string(b))
			continue
		}
		// First file part: stream it while creating the job.
		if mode == jobs.ModeAuto && form["mode"] != "" {
			mode = form["mode"]
		}
		if device == jobs.DeviceAuto && form["device"] != "" {
			device = form["device"]
		}
		if idemKey == "" && form["idempotency_key"] != "" {
			idemKey = form["idempotency_key"]
		}
		if ttlSeconds == 0 && form["ttl_seconds"] != "" {
			ttlSeconds, _ = strconv.Atoi(form["ttl_seconds"])
		}
		job, fresh, cerr := s.Svc.CreateJob(r.Context(), CreateParams{
			OwnerID:        owner(r),
			InputName:      part.FileName(),
			MimeType:       part.Header.Get("Content-Type"),
			Mode:           mode,
			Device:         device,
			Priority:       priority,
			IdempotencyKey: idemKey,
			TTLSeconds:     ttlSeconds,
			Body:           part,
		})
		_ = part.Close()
		if cerr != nil {
			writeErr(w, cerr)
			return
		}
		created, isNew = job, fresh
		break
	}
	if created == nil {
		writeErr(w, NewError(400, jobs.ErrInvalidRequest, "multipart body contains no file part"))
		return
	}
	code := http.StatusAccepted
	if !isNew {
		code = http.StatusOK
	}
	w.Header().Set("Location", "/v1/jobs/"+created.ID)
	writeJSON(w, code, jobView(created, false))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Svc.GetJob(r.Context(), r.PathValue("id"), owner(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, jobView(job, true))
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ResultVersion string `json:"result_version"`
		Action        string `json:"action"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, NewError(400, jobs.ErrInvalidRequest, "invalid JSON body"))
			return
		}
	}
	if v := r.Header.Get("If-Match"); v != "" && body.ResultVersion == "" {
		body.ResultVersion = strings.Trim(v, `"`)
	}
	job, err := s.Svc.Ack(r.Context(), r.PathValue("id"), owner(r), body.ResultVersion, body.Action)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{
		"job_id":           job.ID,
		"retention_status": job.RetentionStatus,
		"result_version":   job.ResultVersion,
		"accepted_at":      nowUTC(),
		"message":          "release accepted; deletion is asynchronous",
	})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	job, err := s.Svc.Cancel(r.Context(), r.PathValue("id"), owner(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{
		"job_id":           job.ID,
		"status":           job.ProcessingStatus,
		"retention_status": job.RetentionStatus,
		"message":          "cancellation accepted",
	})
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	format, ok := documentir.ParseFormat(r.URL.Query().Get("format"))
	if !ok {
		writeErr(w, NewError(400, jobs.ErrInvalidRequest, "format must be one of markdown|text|json"))
		return
	}
	job, leaseID, _, err := s.Svc.BeginDownload(r.Context(), r.PathValue("id"), owner(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	defer s.Svc.EndDownload(context.WithoutCancel(r.Context()), leaseID)

	f, st, manifest, err := s.Svc.OpenResult(job.ID, string(format))
	if err != nil {
		writeErr(w, err)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", contentTypeFor(format))
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("ETag", `"`+manifest.Version+`"`)
	w.Header().Set("X-Result-Version", manifest.Version)
	w.Header().Set("X-Download-Lease", leaseID)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", job.ID+"."+extFor(format)))
	w.Header().Set("Cache-Control", "private, no-store")

	rw := &leaseRenewer{srv: s, leaseID: leaseID, next: time.Now().Add(s.Svc.Cfg.DownloadLeaseTTL / 3)}
	if _, err := rw.copy(w, f); err != nil {
		s.Log.Warn("result stream interrupted", "job", job.ID, "err", err)
	}
}

// leaseRenewer keeps a download lease alive while a body is streaming.
type leaseRenewer struct {
	srv     *Server
	leaseID string
	next    time.Time
}

func (l *leaseRenewer) copy(w http.ResponseWriter, r io.Reader) (int64, error) {
	buf := make([]byte, 64<<10)
	var total int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if time.Now().After(l.next) {
			if _, rerr := l.srv.Svc.RenewDownload(context.Background(), l.leaseID); rerr != nil {
				l.srv.Log.Warn("download lease renew failed", "lease", l.leaseID, "err", rerr)
			}
			l.next = time.Now().Add(l.srv.Svc.Cfg.DownloadLeaseTTL / 3)
		}
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// handleEvents streams job events as SSE with Last-Event-ID resumption.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	job, err := s.Svc.GetJob(r.Context(), jobID, owner(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, NewError(500, jobs.ErrInternal, "streaming unsupported"))
		return
	}
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if v := r.URL.Query().Get("last_event_id"); v != "" {
		lastID, _ = strconv.ParseInt(v, 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()

	send := func(e Event) bool {
		b, _ := json.Marshal(map[string]any{
			"event_id":   e.ID,
			"event_type": e.Type,
			"job_id":     jobID,
			"payload":    e.Payload,
			"created_at": e.CreatedAt,
		})
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Replay the current state first so a fresh client always has context.
	if st, _ := json.Marshal(map[string]any{
		"event_type": "snapshot", "status": job.ProcessingStatus,
		"retention_status": job.RetentionStatus, "progress_percent": job.Progress,
	}); len(st) > 0 {
		if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", st); err != nil {
			return
		}
		flusher.Flush()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			evs, err := s.Svc.Events(r.Context(), jobID, lastID, 200)
			if err != nil {
				return
			}
			for _, e := range evs {
				lastID = e.ID
				if !send(e) {
					return
				}
			}
			cur, err := s.Svc.GetJob(r.Context(), jobID, owner(r))
			if err != nil {
				return
			}
			if cur.ProcessingStatus.Terminal() && len(evs) == 0 {
				// Final state is authoritative via GET /v1/jobs/{id}.
				_, _ = io.WriteString(w, "event: stream_end\ndata: {\"status\":\""+string(cur.ProcessingStatus)+"\"}\n\n")
				flusher.Flush()
				return
			}
		}
	}
}

// handleSyncParse implements POST /v1/parse: same pipeline, bounded wait.
func (s *Server) handleSyncParse(w http.ResponseWriter, r *http.Request) {
	timeout := 30 * time.Second
	if v := r.URL.Query().Get("timeout_ms"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}
	if timeout > 2*time.Minute {
		timeout = 2 * time.Minute
	}
	job, newJob, err := s.createFromRequest(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !newJob {
		writeErr(w, NewError(409, jobs.ErrConflict, "idempotency key already used; use /v1/jobs for async access"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		cur, err := s.Svc.GetJob(ctx, job.ID, owner(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		if cur.ProcessingStatus.Terminal() {
			if cur.ProcessingStatus != jobs.StatusCompleted {
				writeErr(w, NewError(422, cur.ErrorCode, cur.ErrorMessage))
				return
			}
			f, _, manifest, err := s.Svc.OpenResult(cur.ID, "json")
			if err != nil {
				writeErr(w, err)
				return
			}
			defer f.Close()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Result-Version", manifest.Version)
			w.Header().Set("X-Job-Id", cur.ID)
			w.WriteHeader(200)
			_, _ = io.Copy(w, f)
			// Synchronous responses have no retention window: the body was
			// fully written, so release immediately.
			_, _ = s.Svc.Ack(context.WithoutCancel(r.Context()), cur.ID, owner(r), manifest.Version, "release")
			return
		}
		select {
		case <-ctx.Done():
			writeJSON(w, 202, mergeJSON(jobView(cur, true), map[string]any{
				"message": "parse did not finish within the synchronous window; job continues asynchronously",
			}))
			return
		case <-tick.C:
		}
	}
}

func (s *Server) createFromRequest(w http.ResponseWriter, r *http.Request) (*jobs.Job, bool, error) {
	q := r.URL.Query()
	mode := firstNonEmpty(q.Get("mode"), jobs.ModeAuto)
	device := firstNonEmpty(q.Get("device"), jobs.DeviceAuto)
	ttlSeconds, _ := strconv.Atoi(q.Get("ttl_seconds"))
	r.Body = http.MaxBytesReader(w, r.Body, s.Svc.Cfg.MaxUploadBytes+(8<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, false, NewError(400, jobs.ErrInvalidRequest, "expected multipart/form-data body")
	}
	form := map[string]string{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, false, NewError(400, jobs.ErrInvalidRequest, "no file part")
		}
		if err != nil {
			return nil, false, NewError(400, jobs.ErrMalformed, "malformed multipart body")
		}
		if part.FileName() == "" && part.FormName() != "file" {
			b, _ := io.ReadAll(io.LimitReader(part, 64<<10))
			_ = part.Close()
			form[part.FormName()] = strings.TrimSpace(string(b))
			continue
		}
		if form["mode"] != "" {
			mode = form["mode"]
		}
		if form["device"] != "" {
			device = form["device"]
		}
		job, fresh, cerr := s.Svc.CreateJob(r.Context(), CreateParams{
			OwnerID:        owner(r),
			InputName:      part.FileName(),
			MimeType:       part.Header.Get("Content-Type"),
			Mode:           mode,
			Device:         device,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
			TTLSeconds:     ttlSeconds,
			Body:           part,
		})
		_ = part.Close()
		return job, fresh, cerr
	}
}

func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	ws, err := s.Svc.Workers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"workers": ws})
}

// ---------------------------------------------------------------- probes

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "ok", "role": "coordinator"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	checks := map[string]any{}
	ready := true
	if err := s.Svc.DB.PingContext(r.Context()); err != nil {
		checks["sqlite"] = "down: " + err.Error()
		ready = false
	} else {
		checks["sqlite"] = "ok"
	}
	if free, err := s.Svc.Store.FreeBytes(); err != nil {
		checks["storage"] = "error: " + err.Error()
		ready = false
	} else {
		checks["free_bytes"] = free
		checks["storage"] = "ok"
		if free < s.Svc.Store.MinFree {
			checks["storage"] = "below watermark"
			// Still ready: we must serve ACK/reads so space can be reclaimed.
			checks["accepting_uploads"] = false
		} else {
			checks["accepting_uploads"] = true
		}
	}
	lc := s.Svc.LastCleanup()
	checks["last_cleanup"] = lc
	if !lc.IsZero() && time.Since(lc) > 3*s.Svc.Cfg.CleanupInterval {
		checks["cleanup"] = "stalled"
		ready = false
	}
	code := 200
	if !ready {
		code = 503
	}
	writeJSON(w, code, map[string]any{"status": statusWord(ready), "checks": checks})
}

// ---------------------------------------------------------------- worker mux

// InternalMux serves the worker protocol.
func (s *Server) InternalMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /internal/workers/register", s.workerGuard(s.handleRegister))
	m.HandleFunc("POST /internal/workers/{id}/heartbeat", s.workerGuard(s.handleWorkerHeartbeat))
	m.HandleFunc("POST /internal/tasks/claim", s.workerGuard(s.handleClaim))
	m.HandleFunc("GET /internal/tasks/{id}/input", s.workerGuard(s.handleTaskInput))
	m.HandleFunc("POST /internal/tasks/{id}/heartbeat", s.workerGuard(s.handleTaskHeartbeat))
	m.HandleFunc("PUT /internal/tasks/{id}/result", s.workerGuard(s.handleTaskResult))
	m.HandleFunc("POST /internal/tasks/{id}/fail", s.workerGuard(s.handleTaskFail))
	m.HandleFunc("GET /healthz", s.handleHealthz)
	m.HandleFunc("GET /readyz", s.handleReadyz)
	return m
}

func (s *Server) workerGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.InternalToken != "" {
			got := r.Header.Get("X-Internal-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.InternalToken)) != 1 {
				writeErr(w, NewError(401, jobs.ErrUnauthorized, "invalid internal token"))
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var reg WorkerRegistration
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&reg); err != nil {
		writeErr(w, NewError(400, jobs.ErrInvalidRequest, "invalid registration payload"))
		return
	}
	if reg.WorkerID == "" {
		reg.WorkerID = jobs.NewWorkerID()
	}
	if reg.MaxConcurrency <= 0 {
		reg.MaxConcurrency = 1
	}
	if err := s.Svc.RegisterWorker(r.Context(), reg); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"worker_id":       reg.WorkerID,
		"session_expires": nowUTC().Add(5 * time.Minute),
		"heartbeat_every": s.Svc.Cfg.HeartbeatInterval.String(),
	})
}

func (s *Server) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	var reg WorkerRegistration
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&reg); err != nil {
		writeErr(w, NewError(400, jobs.ErrInvalidRequest, "invalid payload"))
		return
	}
	reg.WorkerID = r.PathValue("id")
	if err := s.Svc.RegisterWorker(r.Context(), reg); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	var caps WorkerCaps
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&caps); err != nil {
		writeErr(w, NewError(400, jobs.ErrInvalidRequest, "invalid claim payload"))
		return
	}
	task, err := s.Svc.Claim(r.Context(), caps)
	if err != nil {
		writeErr(w, err)
		return
	}
	if task == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, 200, task)
}

func (s *Server) handleTaskInput(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	token := r.Header.Get("X-Lease-Token")
	if err := s.Svc.checkLease(r.Context(), jobID, token); err != nil {
		writeErr(w, err)
		return
	}
	f, st, err := s.Svc.Store.OpenInput(jobID)
	if err != nil {
		writeErr(w, NewError(410, jobs.ErrGone, "input no longer available"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	if sum, herr := storage.HashFile(s.Svc.Store.InputPath(jobID)); herr == nil {
		w.Header().Set("X-Content-SHA256", sum)
	}
	w.WriteHeader(200)
	if _, err := io.Copy(w, f); err != nil {
		s.Log.Warn("input stream interrupted", "job", jobID, "err", err)
	}
}

func (s *Server) handleTaskHeartbeat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LeaseToken string `json:"lease_token"`
		Progress   int    `json:"progress_percent"`
		Stage      string `json:"stage"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, NewError(400, jobs.ErrInvalidRequest, "invalid payload"))
		return
	}
	if body.Progress == 0 {
		body.Progress = -1
	}
	until, err := s.Svc.Heartbeat(r.Context(), r.PathValue("id"), body.LeaseToken, body.Progress, body.Stage)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"lease_until": until, "renew_every": s.Svc.Cfg.HeartbeatInterval.String()})
}

func (s *Server) handleTaskResult(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	var body struct {
		LeaseToken    string               `json:"lease_token"`
		Engine        string               `json:"engine"`
		EngineVersion string               `json:"engine_version"`
		Device        string               `json:"device_type"`
		PageCount     int                  `json:"page_count"`
		Warnings      int                  `json:"warnings"`
		Document      *documentir.Document `json:"document"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<20)).Decode(&body); err != nil {
		writeErr(w, NewError(400, jobs.ErrMalformed, "invalid result payload: "+err.Error()))
		return
	}
	if body.Document == nil {
		writeErr(w, NewError(400, jobs.ErrMalformed, "missing document"))
		return
	}
	body.Document.Finalize()
	jsonBytes, err := json.Marshal(body.Document)
	if err != nil {
		writeErr(w, NewError(500, jobs.ErrInternal, "encode IR"))
		return
	}
	job, err := s.Svc.Complete(r.Context(), CompleteParams{
		JobID:     jobID,
		Token:     body.LeaseToken,
		Engine:    body.Engine,
		Version:   body.EngineVersion,
		Device:    body.Device,
		Doc:       body.Document,
		Markdown:  []byte(documentir.RenderMarkdown(body.Document)),
		Text:      []byte(documentir.RenderText(body.Document)),
		JSON:      jsonBytes,
		PageCount: body.PageCount,
		Warnings:  len(body.Document.Warnings),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"job_id":            job.ID,
		"status":            job.ProcessingStatus,
		"result_version":    job.ResultVersion,
		"result_expires_at": job.ResultExpiresAt,
	})
}

func (s *Server) handleTaskFail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LeaseToken string `json:"lease_token"`
		Code       string `json:"code"`
		Message    string `json:"message"`
		Retryable  bool   `json:"retryable"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, NewError(400, jobs.ErrInvalidRequest, "invalid payload"))
		return
	}
	if body.Code == "" {
		body.Code = jobs.ErrInternal
	}
	job, err := s.Svc.Fail(r.Context(), r.PathValue("id"), body.LeaseToken, body.Code, body.Message, body.Retryable)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": job.ID, "status": job.ProcessingStatus})
}

// ---------------------------------------------------------------- JSON helpers

func jobView(j *jobs.Job, detail bool) map[string]any {
	base := "/v1/jobs/" + j.ID
	v := map[string]any{
		"job_id":            j.ID,
		"status":            j.ProcessingStatus,
		"processing_status": j.ProcessingStatus,
		"retention_status":  j.RetentionStatus,
		"input_name":        j.InputName,
		"format":            j.Format,
		"mime_type":         j.MimeType,
		"input_size":        j.InputSize,
		"input_sha256":      j.InputSHA256,
		"page_count":        j.PageCount,
		"created_at":        j.CreatedAt,
		"max_lifetime_at":   j.MaxLifetimeAt,
		"attempt":           j.CurrentAttempt,
		"requested_mode":    j.RequestedMode,
		"requested_device":  j.RequestedDevice,
		"engine":            j.ActualEngine,
		"routing_reason":    j.RoutingReason,
		"progress_percent":  j.Progress,
		"progress_stage":    j.ProgressStage,
		"result_expires_at": j.ResultExpiresAt,
		"result_version":    nil,
		"status_url":        base,
		"events_url":        base + "/events",
		"result_urls":       nil,
		"ack_url":           base + "/ack",
		"cancel_url":        base + "/cancel",
	}
	if j.ResultVersion != "" && !j.Gone() {
		v["result_version"] = j.ResultVersion
		v["result_urls"] = map[string]string{
			"markdown": base + "/result?format=markdown",
			"text":     base + "/result?format=text",
			"json":     base + "/result?format=json",
		}
	}
	if j.ErrorCode != "" {
		v["error_code"] = j.ErrorCode
		v["error_message"] = j.ErrorMessage
	}
	if j.WarningCount > 0 {
		v["warning_count"] = j.WarningCount
	}
	if detail {
		v["started_at"] = j.StartedAt
		v["completed_at"] = j.CompletedAt
		v["updated_at"] = j.UpdatedAt
	}
	return v
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	ce := AsError(err)
	if ce.Status == 0 {
		ce.Status = 500
	}
	writeJSON(w, ce.Status, map[string]any{
		"error": map[string]any{
			"code":    ce.Code,
			"message": ce.Message,
		},
	})
}

func contentTypeFor(f documentir.Format) string {
	switch f {
	case documentir.FormatJSON:
		return "application/json; charset=utf-8"
	case documentir.FormatText:
		return "text/plain; charset=utf-8"
	default:
		return "text/markdown; charset=utf-8"
	}
}

func extFor(f documentir.Format) string {
	switch f {
	case documentir.FormatJSON:
		return "json"
	case documentir.FormatText:
		return "txt"
	default:
		return "md"
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func statusWord(ok bool) string {
	if ok {
		return "ready"
	}
	return "not_ready"
}

func mergeJSON(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}

package coordinator

import (
	"compress/gzip"
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
		ctx := context.WithValue(r.Context(), ownerKey{}, owner)
		if rid := r.Header.Get("X-Request-Id"); rid != "" && len(rid) <= 128 {
			ctx = WithRequestID(ctx, rid)
		}
		next(w, r.WithContext(ctx))
	}
}

type ownerKey struct{}

func owner(r *http.Request) string {
	v, _ := r.Context().Value(ownerKey{}).(string)
	return v
}

// ---------------------------------------------------------------- handlers

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	job, isNew, err := s.stageUpload(r, 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	code := http.StatusAccepted
	if !isNew {
		code = http.StatusOK
	}
	w.Header().Set("Location", "/v1/jobs/"+job.ID)
	writeJSON(w, code, jobView(job, false))
}

// stageUpload consumes a multipart body in **any part order** (F06): the file is
// streamed to the coordinator's staging area while scalar fields are collected,
// and the job is created only after the whole body has been read. Nothing is
// buffered in memory beyond one part's header.
//
// Precedence: query parameters are authoritative for mode/device/priority/ttl;
// multipart fields fill in the gaps. Both are documented in the OpenAPI spec.
func (s *Server) stageUpload(r *http.Request, maxBodyLimit int64) (*jobs.Job, bool, error) {
	q := r.URL.Query()
	form := map[string]string{}
	ownerID := owner(r)

	jobID := jobs.NewJobID()
	writer, err := s.Svc.StageInput(jobID)
	if err != nil {
		return nil, false, err
	}
	committed := false
	defer func() {
		if !committed {
			writer.Abort()
		}
	}()

	limit := maxBodyLimit
	if limit <= 0 {
		limit = s.Svc.Cfg.MaxUploadBytes + (8 << 20) // multipart overhead
	}
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, false, NewError(400, jobs.ErrInvalidRequest, "expected multipart/form-data body")
	}

	var fileName, mimeType string
	haveFile := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				return nil, false, NewError(413, jobs.ErrTooLarge,
					fmt.Sprintf("upload exceeds %d bytes", s.Svc.Cfg.MaxUploadBytes))
			}
			return nil, false, NewError(400, jobs.ErrMalformed, "malformed multipart body: "+err.Error())
		}
		name := part.FormName()
		if part.FileName() == "" && name != "file" {
			b, rerr := io.ReadAll(io.LimitReader(part, 64<<10))
			_ = part.Close()
			if rerr != nil {
				return nil, false, NewError(400, jobs.ErrMalformed, "read form field "+name)
			}
			form[name] = strings.TrimSpace(string(b))
			continue
		}
		if haveFile {
			_ = part.Close()
			return nil, false, NewError(400, jobs.ErrInvalidRequest, "multipart body contains more than one file part")
		}
		haveFile = true
		fileName = part.FileName()
		mimeType = part.Header.Get("Content-Type")
		if _, err := io.Copy(writer, part); err != nil {
			_ = part.Close()
			if errors.Is(err, storage.ErrTooLarge) {
				return nil, false, NewError(413, jobs.ErrTooLarge,
					fmt.Sprintf("upload exceeds %d bytes", s.Svc.Cfg.MaxUploadBytes))
			}
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				return nil, false, NewError(413, jobs.ErrTooLarge,
					fmt.Sprintf("upload exceeds %d bytes", s.Svc.Cfg.MaxUploadBytes))
			}
			return nil, false, NewError(400, jobs.ErrInvalidRequest, "upload interrupted: "+err.Error())
		}
		_ = part.Close()
	}
	if !haveFile {
		return nil, false, NewError(400, jobs.ErrInvalidRequest, "multipart body contains no file part")
	}

	mode := firstNonEmpty(q.Get("mode"), form["mode"], jobs.ModeAuto)
	device := firstNonEmpty(q.Get("device"), form["device"], jobs.DeviceAuto)
	idemKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), q.Get("idempotency_key"), form["idempotency_key"])
	priority, _ := strconv.Atoi(firstNonEmpty(q.Get("priority"), form["priority"]))
	ttlSeconds, _ := strconv.Atoi(firstNonEmpty(q.Get("ttl_seconds"), form["ttl_seconds"]))

	job, isNew, err := s.Svc.CreateJobStaged(r.Context(), CreateParams{
		OwnerID:        ownerID,
		InputName:      fileName,
		MimeType:       mimeType,
		Mode:           mode,
		Device:         device,
		Priority:       priority,
		IdempotencyKey: idemKey,
		TTLSeconds:     ttlSeconds,
	}, jobID, writer)
	if err != nil {
		return nil, false, err
	}
	committed = true
	return job, isNew, nil
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
	var body struct {
		ResultVersion string `json:"result_version"`
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
	job, err := s.Svc.Cancel(r.Context(), r.PathValue("id"), owner(r), body.ResultVersion)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{
		"job_id":                 job.ID,
		"status":                 job.ProcessingStatus,
		"retention_status":       job.RetentionStatus,
		"result_version":         job.ResultVersion,
		"processing_deadline_at": job.MaxLifetimeAt,
		"message":                "cancellation accepted",
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

	// F05: the ETag validates *this* representation's bytes. The manifest hash
	// covers all three artifacts and is only the ACK token (X-Result-Version),
	// never a strong validator for one representation.
	etag := `"` + artifactETag(manifest, artifactName(string(format))) + `"`
	// gzip is applied on the wire only (artifacts stay uncompressed): the encoded
	// representation is semantically equivalent, so its validator is weak.
	useGzip := st.Size() > 1024 && acceptsGzip(r.Header.Get("Accept-Encoding"))
	respETag := etag
	if useGzip {
		respETag = "W/" + etag
	}
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, respETag) {
		s.Svc.EndDownload(context.WithoutCancel(r.Context()), leaseID)
		w.Header().Set("ETag", respETag)
		w.Header().Set("X-Result-Version", manifest.Version)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", contentTypeFor(format))
	w.Header().Set("ETag", respETag)
	w.Header().Set("X-Result-Version", manifest.Version)
	w.Header().Set("Vary", "Accept-Encoding")
	if useGzip {
		w.Header().Set("Content-Encoding", "gzip")
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	w.Header().Set("X-Download-Lease", leaseID)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", job.ID+"."+extFor(format)))
	w.Header().Set("Cache-Control", "private, no-store")

	rw := &leaseRenewer{srv: s, leaseID: leaseID, next: time.Now().Add(s.Svc.Cfg.DownloadLeaseTTL / 3)}
	var dst io.Writer = w
	var gz *gzip.Writer
	if useGzip {
		gz = gzip.NewWriter(w)
		dst = gz
	}
	if _, err := rw.copyTo(dst, f); err != nil {
		s.Log.Warn("result stream interrupted", "job", job.ID, "err", err)
	}
	if gz != nil {
		_ = gz.Close()
	}
}

// leaseRenewer keeps a download lease alive while a body is streaming.
type leaseRenewer struct {
	srv     *Server
	leaseID string
	next    time.Time
}

func (l *leaseRenewer) copyTo(w io.Writer, r io.Reader) (int64, error) {
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
//
// F07: the subscription scope is explicit.
//   - scope=processing (default): the stream ends when the *processing* status is
//     terminal (completed/failed/cancelled). Retention events are not sent.
//   - scope=retention: the stream continues into the retention lifecycle and ends
//     at released/expired (or after sseMaxDuration).
//
// A Last-Event-ID older than the oldest retained event (events are deleted with
// the job row after the tombstone window) gets a `cursor_reset` event carrying a
// fresh snapshot, never a silent gap.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = "processing"
	}
	if scope != "processing" && scope != "retention" {
		writeErr(w, NewError(400, jobs.ErrInvalidRequest, "scope must be processing|retention"))
		return
	}
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
	w.Header().Set("X-SSE-Scope", scope)
	w.WriteHeader(200)
	flusher.Flush()

	snapshot := func(reason string) bool {
		st, _ := json.Marshal(map[string]any{
			"event_type": "snapshot", "status": job.ProcessingStatus,
			"retention_status": job.RetentionStatus, "progress_percent": job.Progress,
			"reason": reason,
		})
		if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", st); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
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

	if !snapshot("initial") {
		return
	}
	if lastID > 0 {
		if oldest, oerr := s.Svc.OldestEventID(r.Context(), jobID); oerr == nil &&
			oldest > 0 && lastID < oldest-1 {
			if _, err := fmt.Fprintf(w, "event: cursor_reset\ndata: {\"reason\":\"cursor older than the retained window\",\"oldest_event_id\":%d}\n\n", oldest); err != nil {
				return
			}
			flusher.Flush()
			lastID = 0
		}
	}

	deadline := time.Now().Add(sseMaxDuration)
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
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
		case <-poll.C:
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
				// Job row purged (tombstone over): the stream is over.
				return
			}
			done := cur.ProcessingStatus.Terminal() && len(evs) == 0
			if scope == "retention" && done && !cur.RetentionStatus.Terminal() {
				done = false
			}
			if done {
				_, _ = fmt.Fprintf(w, "event: stream_end\ndata: {\"status\":%q,\"retention_status\":%q,\"scope\":%q}\n\n",
					cur.ProcessingStatus, cur.RetentionStatus, scope)
				flusher.Flush()
				return
			}
			if time.Now().After(deadline) {
				_, _ = io.WriteString(w, "event: stream_end\ndata: {\"reason\":\"max_duration\"}\n\n")
				flusher.Flush()
				return
			}
		}
	}
}

// sseMaxDuration bounds one SSE connection so a client cannot hold a server
// goroutine (and its DB polling) forever.
const sseMaxDuration = 10 * time.Minute

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
	job, newJob, err := s.stageUpload(r, s.Svc.Cfg.MaxUploadBytes+(8<<20))
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
	m.HandleFunc("GET /internal/metrics", s.MetricsHandler())
	m.HandleFunc("POST /internal/workers/register", s.workerGuard(s.handleRegister))
	m.HandleFunc("POST /internal/workers/{id}/heartbeat", s.workerGuard(s.handleWorkerHeartbeat))
	m.HandleFunc("POST /internal/tasks/claim", s.workerGuard(s.handleClaim))
	m.HandleFunc("GET /internal/tasks/{id}/input", s.workerGuard(s.handleTaskInput))
	m.HandleFunc("POST /internal/tasks/{id}/heartbeat", s.workerGuard(s.handleTaskHeartbeat))
	m.HandleFunc("PUT /internal/tasks/{id}/result", s.workerGuard(s.handleTaskResult))
	m.HandleFunc("POST /internal/tasks/{id}/fail", s.workerGuard(s.handleTaskFail))
	// Operator diagnostics belong to the internal surface, not the public mux.
	m.HandleFunc("GET /internal/diagnostics/workers", s.workerGuard(s.handleWorkers))
	m.HandleFunc("GET /healthz", s.handleHealthz)
	m.HandleFunc("GET /readyz", s.handleReadyz)
	return m
}

// internalGuard protects operator endpoints that need the internal token but no owner
// identity (metrics, diagnostics).
func (s *Server) internalGuard(next http.HandlerFunc) http.HandlerFunc {
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
		"job_id":              j.ID,
		"status":              j.ProcessingStatus,
		"processing_status":   j.ProcessingStatus,
		"retention_status":    j.RetentionStatus,
		"input_name":          j.InputName,
		"format":              j.Format,
		"mime_type":           j.MimeType,
		"input_size":          j.InputSize,
		"input_sha256":        j.InputSHA256,
		"page_count":          j.PageCount,
		"created_at":          j.CreatedAt,
		"max_lifetime_at":     j.MaxLifetimeAt,
		"attempt":             j.CurrentAttempt,
		"requested_mode":      j.RequestedMode,
		"requested_device":    j.RequestedDevice,
		"engine":              j.ActualEngine,
		"routing_reason":      j.RoutingReason,
		"progress_percent":    j.Progress,
		"progress_stage":      j.ProgressStage,
		"cancel_requested_at": j.CancelRequestedAt,
		"result_expires_at":   j.ResultExpiresAt,
		"result_version":      nil,
		"status_url":          base,
		"events_url":          base + "/events",
		"result_urls":         nil,
		"ack_url":             base + "/ack",
		"cancel_url":          base + "/cancel",
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
	if ce.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ce.RetryAfter))
	}
	// RC-5: make throttling observable so an operator can answer "why was this
	// tenant refused" without reading its logs.
	if reason := throttleReason(ce.Code); reason != "" {
		defaultInstruments.throttled.With(reason).Inc()
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

// artifactETag returns the strong validator for one representation. The manifest
// stores a sha256 per artifact, so each format has its own ETag while
// X-Result-Version (the manifest hash) stays identical and is what ACK accepts.
func artifactETag(m *storage.Manifest, name string) string {
	if m != nil {
		for _, a := range m.Files {
			if a.Name == name {
				return "sha256:" + a.SHA256
			}
		}
	}
	return "sha256:unknown"
}

// etagMatches implements the weak comparison rules of RFC 9110 §13.1.2 for our
// strong ETags (only exact matches count; "*" matches any existing result).
// etagMatches compares validators, tolerating the weak prefix on either side
// (a gzipped response carries W/"…" for the same artifact).
func etagMatches(header, etag string) bool {
	norm := func(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "W/") }
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			return true
		}
		if norm(part) == norm(etag) {
			return true
		}
	}
	return false
}

// acceptsGzip honours quality values: "gzip;q=0" explicitly rejects gzip.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		q := 1.0
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, "q=") {
				if v, err := strconv.ParseFloat(strings.TrimPrefix(f, "q="), 64); err == nil {
					q = v
				}
			}
		}
		return q > 0
	}
	return false
}

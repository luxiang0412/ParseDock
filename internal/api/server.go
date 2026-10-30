// Package api implements the stateless public gateway.
//
// An API replica owns no files and no job state: it authenticates the caller,
// applies rate limits and upload caps, injects the resolved owner identity and
// reverse-proxies to the single Coordinator. Any replica can therefore serve
// any job, and replicas can be killed or added freely.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"parsedock.dev/parsedock/internal/tlsutil"
)

// Config configures the gateway.
type Config struct {
	CoordinatorURL string
	InternalToken  string
	// Keys are hashed API credentials (see keys.go). Plaintext tokens are not
	// retained after startup.
	Keys           []Key
	DevInsecure    bool
	RateLimitRPS   float64
	RateBurst      int
	MaxUploadBytes int64
	// TLS is the client configuration used towards the coordinator (mTLS when it
	// carries a client certificate).
	TLS *tls.Config
	Log *slog.Logger
}

// Server is the HTTP gateway.
type Server struct {
	cfg      Config
	proxy    *httputil.ReverseProxy
	limiter  *limiter
	log      *slog.Logger
	throttle throttleForward
}

// New builds the gateway.
func New(cfg Config) (*Server, error) {
	target, err := url.Parse(cfg.CoordinatorURL)
	if err != nil {
		return nil, err
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.RateLimitRPS <= 0 {
		cfg.RateLimitRPS = 20
	}
	if cfg.RateBurst <= 0 {
		cfg.RateBurst = 40
	}
	s := &Server{cfg: cfg, log: cfg.Log, limiter: newLimiter(cfg.RateLimitRPS, cfg.RateBurst)}
	transport := tlsutil.Transport(cfg.TLS)

	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			// Never trust client-supplied internal headers.
			r.Out.Header.Del("X-Internal-Token")
			r.Out.Header.Del("X-Parsedock-Owner")
			r.Out.Header.Set("X-Internal-Token", cfg.InternalToken)
			r.Out.Header.Set("X-Parsedock-Owner", ownerFrom(r.In))
			r.Out.Header.Set("X-Request-Id", requestID(r.In))
		},
		// Never trust a hop-by-hop header from upstream into the client.
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("X-Content-Type-Options", "nosniff")
			if cd := resp.Header.Get("Content-Disposition"); cd != "" {
				// The coordinator generates this header itself, but strip any
				// control characters defensively (header injection guard).
				resp.Header.Set("Content-Disposition", sanitizeHeaderValue(cd))
			}
			return nil
		},
		Transport: transport,
		// Stream responses immediately: results and SSE must not be buffered.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.log.Warn("coordinator unreachable", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": map[string]any{
					"code":    "coordinator_unavailable",
					"message": "the coordinator is unreachable; retry with backoff",
				},
			})
		},
	}
	return s, nil
}

// Handler returns the public mux, wrapped with the edge headers.
func (s *Server) Handler() http.Handler {
	return s.edgeHeaders(s.mux())
}

// edgeHeaders sets the security/correlation headers for every response, including
// streamed and proxied ones (the reverse proxy replaces the header map, so setting
// them before ServeHTTP is not enough).
func (s *Server) edgeHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-Id")
		if !validRequestID(reqID) {
			reqID = newRequestID()
			r.Header.Set("X-Request-Id", reqID)
		}
		h := &headerWriter{ResponseWriter: w, headers: map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Request-Id":           reqID,
		}}
		next.ServeHTTP(h, r)
	})
}

// headerWriter injects edge headers on the first WriteHeader (or Write) and keeps
// http.Flusher working so SSE and result streaming are unaffected.
type headerWriter struct {
	http.ResponseWriter
	headers map[string]string
	wrote   bool
}

func (h *headerWriter) WriteHeader(code int) {
	if !h.wrote {
		for k, v := range h.headers {
			h.ResponseWriter.Header().Set(k, v)
		}
		h.wrote = true
	}
	h.ResponseWriter.WriteHeader(code)
}

func (h *headerWriter) Write(p []byte) (int, error) {
	if !h.wrote {
		h.WriteHeader(http.StatusOK)
	}
	return h.ResponseWriter.Write(p)
}

func (h *headerWriter) Flush() {
	if f, ok := h.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) mux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", s.handleHealthz)
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		s.forward(w, r, "/readyz")
	})
	m.HandleFunc("GET /v1/", s.auth(s.limit(s.handleV1)))
	m.HandleFunc("POST /v1/", s.auth(s.limit(s.handleV1)))
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"code": "not_found", "message": "unknown path"},
		})
	})
	return m
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "role": "api"})
}

func (s *Server) handleV1(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && (r.URL.Path == "/v1/jobs" || r.URL.Path == "/v1/parse") {
		if s.cfg.MaxUploadBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+(8<<20))
		}
	}
	s.proxy.ServeHTTP(w, r)
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, path string) {
	clone := r.Clone(r.Context())
	clone.URL.Path = path
	clone.Header.Del("X-Internal-Token")
	clone.Header.Del("X-Parsedock-Owner")
	s.proxy.ServeHTTP(w, clone)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			token = r.Header.Get("X-Api-Key")
		}
		if len(s.cfg.Keys) == 0 {
			if !s.cfg.DevInsecure {
				writeJSON(w, 401, map[string]any{"error": map[string]any{
					"code": "unauthorized", "message": "no API keys configured; use --api-keys-file"}})
				return
			}
			owner := r.Header.Get("X-Debug-Owner")
			if owner == "" {
				owner = "dev"
			}
			setOwner(r, owner)
			next(w, r)
			return
		}
		key, ok := Lookup(s.cfg.Keys, token)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="parsedock"`)
			writeJSON(w, 401, map[string]any{"error": map[string]any{
				"code": "unauthorized", "message": "invalid or missing API token"}})
			return
		}
		if need := NeededScope(r.Method, r.URL.Path); !key.HasScope(need) {
			writeJSON(w, 403, map[string]any{"error": map[string]any{
				"code":    "insufficient_scope",
				"message": "this API key does not grant " + need,
			}})
			return
		}
		setOwner(r, key.Owner)
		next(w, r)
	}
}

// validRequestID accepts a conservative charset so a client cannot inject log
// separators or header garbage through the correlation id.
func validRequestID(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(b[:])
}

// throttleForward batches a replica's refusals and ships them to the coordinator: a
// replica is stateless and has no scrape endpoint of its own, so the documented
// parsedock_throttled_total series must be aggregated centrally (RC-5).
type throttleForward struct {
	mu      sync.Mutex
	pending map[string]int64
}

func (t *throttleForward) add(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending = map[string]int64{}
	}
	t.pending[reason]++
}

func (t *throttleForward) drain() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.pending
	t.pending = map[string]int64{}
	return out
}

func (t *throttleForward) restore(m map[string]int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending = map[string]int64{}
	}
	for k, v := range m {
		t.pending[k] += v
	}
}

func (s *Server) limit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(ownerFrom(r)) {
			w.Header().Set("Retry-After", "1")
			s.throttle.add("rate_limited")
			writeJSON(w, 429, map[string]any{"error": map[string]any{
				"code": "rate_limited", "message": "too many requests for this owner"}})
			return
		}
		next(w, r)
	}
}

// sanitizeHeaderValue removes CR/LF and other control bytes from a header value.
func sanitizeHeaderValue(v string) string {
	var b strings.Builder
	for _, c := range v {
		if c == '\r' || c == '\n' || c < 0x20 && c != '\t' {
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

type ownerCtxKey struct{}

func setOwner(r *http.Request, owner string) {
	*r = *r.WithContext(contextWithOwner(r.Context(), owner))
}

func ownerFrom(r *http.Request) string {
	if v, ok := r.Context().Value(ownerCtxKey{}).(string); ok {
		return v
	}
	return ""
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func requestID(r *http.Request) string {
	if v := r.Header.Get("X-Request-Id"); v != "" {
		return v
	}
	return strings.TrimPrefix(time.Now().UTC().Format("20060102T150405.000000"), "") + "-" + r.RemoteAddr
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// limiter is a per-owner token bucket.
type limiter struct {
	mu     sync.Mutex
	rps    float64
	burst  float64
	bucket map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rps float64, burst int) *limiter {
	return &limiter{rps: rps, burst: float64(burst), bucket: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	if l.rps <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.bucket[key]
	now := time.Now()
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.bucket[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += elapsed * l.rps
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func contextWithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerCtxKey{}, owner)
}

// FlushThrottle ships batched refusals to the coordinator until ctx is cancelled. The
// flush is best-effort: a failed post is merged back into the pending map, so a
// coordinator restart loses no count.
func (s *Server) FlushThrottle(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pending := s.throttle.drain()
		if len(pending) == 0 {
			continue
		}
		body, err := json.Marshal(map[string]any{"counts": pending})
		if err != nil {
			s.throttle.restore(pending)
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "POST",
			strings.TrimRight(s.cfg.CoordinatorURL, "/")+"/internal/throttle", bytes.NewReader(body))
		if err != nil {
			s.throttle.restore(pending)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if s.cfg.InternalToken != "" {
			req.Header.Set("X-Internal-Token", s.cfg.InternalToken)
		}
		resp, err := s.proxy.Transport.RoundTrip(req)
		if err != nil {
			s.throttle.restore(pending)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			s.throttle.restore(pending)
		}
	}
}

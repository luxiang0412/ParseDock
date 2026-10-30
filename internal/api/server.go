// Package api implements the stateless public gateway.
//
// An API replica owns no files and no job state: it authenticates the caller,
// applies rate limits and upload caps, injects the resolved owner identity and
// reverse-proxies to the single Coordinator. Any replica can therefore serve
// any job, and replicas can be killed or added freely.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config configures the gateway.
type Config struct {
	CoordinatorURL string
	InternalToken  string
	// Keys maps bearer tokens to owner IDs.
	Keys           map[string]string
	DevInsecure    bool
	RateLimitRPS   float64
	RateBurst      int
	MaxUploadBytes int64
	Log            *slog.Logger
}

// Server is the HTTP gateway.
type Server struct {
	cfg     Config
	proxy   *httputil.ReverseProxy
	limiter *limiter
	log     *slog.Logger
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

// Handler returns the public mux.
func (s *Server) Handler() http.Handler {
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
					"code": "unauthorized", "message": "no API keys configured; set PARSEDOCK_API_KEYS"}})
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
		for key, owner := range s.cfg.Keys {
			if subtle.ConstantTimeCompare([]byte(key), []byte(token)) == 1 {
				setOwner(r, owner)
				next(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="parsedock"`)
		writeJSON(w, 401, map[string]any{"error": map[string]any{
			"code": "unauthorized", "message": "invalid or missing API token"}})
	}
}

func (s *Server) limit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(ownerFrom(r)) {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, 429, map[string]any{"error": map[string]any{
				"code": "rate_limited", "message": "too many requests for this owner"}})
			return
		}
		next(w, r)
	}
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

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
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
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
	Keys        []Key
	DevInsecure bool
	// RateLimitRPS is the per-owner token-bucket rate. 0 disables the limiter; a
	// negative value is a configuration error. The bucket lives in this replica, so N
	// replicas behind a load balancer admit up to N times the rate.
	RateLimitRPS   float64
	RateBurst      int
	MaxUploadBytes int64
	// TLS is the client configuration used towards the coordinator (mTLS when it
	// carries a client certificate).
	TLS *tls.Config
	Log *slog.Logger
	// ProxyResponseHeaderTimeout bounds how long the gateway waits for the coordinator
	// to start a response. It must exceed the synchronous POST /v1/parse window
	// (two minutes); 0 selects DefaultProxyResponseHeaderTimeout.
	ProxyResponseHeaderTimeout time.Duration
	// BodyReadTimeout bounds reading one request body (an upload). 0 selects
	// DefaultBodyReadTimeout, a negative value disables the deadline.
	BodyReadTimeout time.Duration
	// TrustedProxies are the networks whose X-Forwarded-For is believed. Empty (the
	// default) means the TCP peer is the client.
	TrustedProxies []netip.Prefix
}

const (
	// syncParseMaxWait mirrors the coordinator's cap on POST /v1/parse?timeout_ms.
	syncParseMaxWait = 2 * time.Minute
	// DefaultProxyResponseHeaderTimeout leaves 30 s of headroom above the synchronous
	// parse window for staging and validating the upload. The worker transport keeps
	// tlsutil.WorkerResponseHeaderTimeout (30 s); using it here turned every sync parse
	// longer than 30 s into a 502 at the gateway.
	DefaultProxyResponseHeaderTimeout = syncParseMaxWait + 30*time.Second

	// Failed authentication is limited per client IP before any owner is known, so a
	// flood of bad tokens costs the sender a 429 instead of free 401s.
	authFailRPS   = 1.0
	authFailBurst = 20
	// maxLimiterKeys bounds every limiter map; idle buckets are evicted first.
	maxLimiterKeys = 10000
)

// Headers the gateway owns. A client value is always dropped before proxying.
const (
	headerInternalToken = "X-Internal-Token"
	headerOwner         = "X-Parsedock-Owner"
	// HeaderClientIP carries the resolved client address to the coordinator, which
	// believes it only together with the internal token.
	HeaderClientIP = "X-Parsedock-Client-Ip"
)

// Server is the HTTP gateway.
type Server struct {
	cfg      Config
	proxy    *httputil.ReverseProxy
	limiter  *limiter
	authFail *limiter
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
	if cfg.RateLimitRPS < 0 {
		return nil, fmt.Errorf("rate limit must be >= 0 (0 disables it), got %v", cfg.RateLimitRPS)
	}
	if cfg.RateLimitRPS == 0 {
		cfg.Log.Warn("per-owner rate limiting is disabled (--rate-limit=0)")
	}
	if cfg.RateBurst <= 0 {
		cfg.RateBurst = 40
	}
	if cfg.ProxyResponseHeaderTimeout <= 0 {
		cfg.ProxyResponseHeaderTimeout = DefaultProxyResponseHeaderTimeout
	}
	if cfg.BodyReadTimeout == 0 {
		cfg.BodyReadTimeout = DefaultBodyReadTimeout
	}
	s := &Server{
		cfg: cfg, log: cfg.Log,
		limiter:  newLimiter(cfg.RateLimitRPS, cfg.RateBurst, maxLimiterKeys),
		authFail: newLimiter(authFailRPS, authFailBurst, maxLimiterKeys),
	}
	// A dedicated transport: the worker RPC transport's 30 s header timeout is shorter
	// than the synchronous parse window.
	transport := tlsutil.TransportWithResponseHeaderTimeout(cfg.TLS, cfg.ProxyResponseHeaderTimeout)

	s.proxy = &httputil.ReverseProxy{
		// Rewrite (unlike Director) starts from an outbound request without Forwarded /
		// X-Forwarded-* headers, so nothing the client claimed about its address survives.
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			// Never trust client-supplied internal headers, and never hand the client's
			// own credential to the coordinator: it authenticates the gateway, not the key.
			for _, h := range []string{headerInternalToken, headerOwner, HeaderClientIP,
				"X-Debug-Owner", "Authorization", "X-Api-Key"} {
				r.Out.Header.Del(h)
			}
			if ip := clientIPFrom(r.In); ip != "" {
				r.Out.Header.Set(HeaderClientIP, ip)
				r.Out.Header.Set("X-Forwarded-For", ip)
			}
			// edgeHeaders guarantees a validated id on every request.
			r.Out.Header.Set("X-Request-Id", r.In.Header.Get("X-Request-Id"))
			if isProbe(r.In) {
				// Probes are unauthenticated: forward them without the internal token so
				// the coordinator answers with its redacted readiness view.
				return
			}
			r.Out.Header.Set(headerInternalToken, cfg.InternalToken)
			r.Out.Header.Set(headerOwner, ownerFrom(r.In))
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

// Handler returns the public mux, wrapped with the edge headers and the body read
// deadline.
func (s *Server) Handler() http.Handler {
	return BodyReadDeadline(s.edgeHeaders(s.mux()), s.cfg.BodyReadTimeout)
}

// edgeHeaders sets the security/correlation headers for every response, including
// streamed and proxied ones (the reverse proxy replaces the header map, so setting
// them before ServeHTTP is not enough). It also resolves the client address once.
func (s *Server) edgeHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-Id")
		if !validRequestID(reqID) {
			reqID = newRequestID()
			r.Header.Set("X-Request-Id", reqID)
		}
		r = r.WithContext(context.WithValue(r.Context(), clientIPKey{}, s.clientIP(r)))
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

// Unwrap lets http.ResponseController reach the connection (read deadlines).
func (h *headerWriter) Unwrap() http.ResponseWriter { return h.ResponseWriter }

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

// forward proxies an unauthenticated probe. It carries no owner and no internal token
// (see the proxy's Rewrite), so the coordinator returns only its public view.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, path string) {
	clone := r.Clone(context.WithValue(r.Context(), probeKey{}, true))
	clone.URL.Path = path
	s.proxy.ServeHTTP(w, clone)
}

type probeKey struct{}

func isProbe(r *http.Request) bool {
	v, _ := r.Context().Value(probeKey{}).(bool)
	return v
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIPFrom(r)
		// An address that keeps failing authentication is refused before its token is
		// even looked at: failures are cheap for the sender, so they must not be free.
		if s.authFail.blocked(ip) {
			s.rejectAuthFlood(w)
			return
		}
		token := bearer(r)
		if token == "" {
			token = r.Header.Get("X-Api-Key")
		}
		if len(s.cfg.Keys) == 0 {
			if !s.cfg.DevInsecure {
				s.authFail.allow(ip)
				writeJSON(w, 401, map[string]any{"error": map[string]any{
					"code": "unauthorized", "message": "no API keys configured; use --api-keys-file"}})
				return
			}
			// Development only. The value is validated so a client cannot mint
			// arbitrary owners/limiter keys or inject log separators.
			owner := r.Header.Get("X-Debug-Owner")
			if !validOwner(owner) {
				owner = "dev"
			}
			setOwner(r, owner)
			next(w, r)
			return
		}
		key, ok := Lookup(s.cfg.Keys, token)
		if !ok {
			if !s.authFail.allow(ip) {
				s.rejectAuthFlood(w)
				return
			}
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

func (s *Server) rejectAuthFlood(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	s.throttle.add("rate_limited")
	writeJSON(w, 429, map[string]any{"error": map[string]any{
		"code": "rate_limited", "message": "too many failed authentication attempts from this address"}})
}

// validOwner bounds a development owner id (X-Debug-Owner).
func validOwner(v string) bool { return len(v) <= 64 && validRequestID(v) }

type clientIPKey struct{}

func clientIPFrom(r *http.Request) string {
	v, _ := r.Context().Value(clientIPKey{}).(string)
	return v
}

// clientIP resolves the caller's address. The TCP peer is the client unless it is one
// of the configured trusted proxies; then X-Forwarded-For is walked from the right and
// the first address that is not a trusted proxy wins (the left part of the header is
// client-controlled and never believed on its own).
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	addr = addr.Unmap()
	if !s.trusted(addr) {
		return addr.String()
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // malformed: stop at the last address we can vouch for
		}
		hop = hop.Unmap()
		if !s.trusted(hop) {
			return hop.String()
		}
		addr = hop
	}
	return addr.String()
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ParseTrustedProxies parses a comma-separated list of CIDRs or bare addresses.
func ParseTrustedProxies(spec string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			a, err := netip.ParseAddr(part)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", part, err)
			}
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", part, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// limiter is a keyed token bucket (per owner, or per client IP for failed auth). The
// map is bounded: when it is full, buckets that have refilled completely (and are
// therefore indistinguishable from a new key) are evicted first, then a slice of
// arbitrary ones, so a caller minting keys cannot grow memory without bound.
type limiter struct {
	mu     sync.Mutex
	rps    float64
	burst  float64
	max    int
	bucket map[string]*bucket
	now    func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rps float64, burst, max int) *limiter {
	if max <= 0 {
		max = maxLimiterKeys
	}
	return &limiter{rps: rps, burst: float64(burst), max: max, bucket: map[string]*bucket{}, now: time.Now}
}

func (l *limiter) refill(b *bucket, now time.Time) {
	b.tokens += now.Sub(b.last).Seconds() * l.rps
	b.last = now
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
}

// allow consumes one token for key.
func (l *limiter) allow(key string) bool {
	if l == nil || l.rps <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.bucket[key]
	if !ok {
		l.makeRoom(now)
		b = &bucket{tokens: l.burst, last: now}
		l.bucket[key] = b
	}
	l.refill(b, now)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// blocked reports whether key has no token left, without consuming one and without
// creating a bucket for an unknown key.
func (l *limiter) blocked(key string) bool {
	if l == nil || l.rps <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.bucket[key]
	if !ok {
		return false
	}
	l.refill(b, l.now())
	return b.tokens < 1
}

func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bucket)
}

// makeRoom must be called with l.mu held.
func (l *limiter) makeRoom(now time.Time) {
	if len(l.bucket) < l.max {
		return
	}
	for k, b := range l.bucket {
		if b.tokens+now.Sub(b.last).Seconds()*l.rps >= l.burst {
			delete(l.bucket, k)
		}
	}
	if len(l.bucket) < l.max {
		return
	}
	// Everything is busy: drop a tenth in map order, so the next sweeps are amortised
	// instead of running on every new key.
	n := l.max/10 + 1
	for k := range l.bucket {
		if n == 0 {
			break
		}
		delete(l.bucket, k)
		n--
	}
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
		// Bound the whole exchange: a coordinator that accepts the connection but stalls
		// would otherwise block this goroutine (and therefore all later reports) forever.
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		body, err := json.Marshal(map[string]any{"counts": pending})
		if err != nil {
			cancel()
			s.throttle.restore(pending)
			continue
		}
		req, err := http.NewRequestWithContext(fctx, "POST",
			strings.TrimRight(s.cfg.CoordinatorURL, "/")+"/internal/throttle", bytes.NewReader(body))
		if err != nil {
			cancel()
			s.throttle.restore(pending)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if s.cfg.InternalToken != "" {
			req.Header.Set("X-Internal-Token", s.cfg.InternalToken)
		}
		resp, err := s.proxy.Transport.RoundTrip(req)
		if err != nil {
			cancel()
			s.throttle.restore(pending)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		cancel()
		if resp.StatusCode/100 != 2 {
			s.throttle.restore(pending)
		}
	}
}

package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testInternal = "internal-token-0123456789abcdef"
	testKey      = "client-key-0123456789abcdef0123"
)

// upstream records what the gateway forwarded.
type upstream struct {
	mu   sync.Mutex
	last http.Header
	path string
	srv  *httptest.Server
}

func newUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.last = r.Header.Clone()
		u.path = r.URL.Path
		u.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) header() http.Header {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.last
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newGateway(t *testing.T, up *upstream, tweak func(*Config)) http.Handler {
	t.Helper()
	keys, err := ParseKeyEntries([]string{
		testKey + ":acme",
		"read-only-key-0123456789abcdef:acme:jobs:read",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{CoordinatorURL: up.srv.URL, InternalToken: testInternal, Keys: keys,
		RateLimitRPS: 1000, RateBurst: 1000, Log: quietLog()}
	if tweak != nil {
		tweak(&cfg)
	}
	gw, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return gw.Handler()
}

func call(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.0.2.10:5555"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthRejectsMissingAndUnknownTokens(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, nil)
	for _, hdr := range []map[string]string{
		nil,
		{"Authorization": "Bearer wrong-token-0123456789"},
		{"Authorization": "Bearer "},
		{"X-Api-Key": "wrong-token-0123456789"},
	} {
		rec := call(h, "GET", "/v1/jobs/job_x", hdr)
		if rec.Code != 401 {
			t.Fatalf("%v: code %d, want 401", hdr, rec.Code)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("X-Request-Id") == "" {
			t.Fatalf("edge headers missing on 401: %v", rec.Header())
		}
	}
	if up.header() != nil {
		t.Fatal("an unauthenticated request reached the coordinator")
	}
}

func TestAuthForwardsOwnerAndStripsClientCredentials(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, nil)
	for _, hdr := range []map[string]string{
		{"Authorization": "Bearer " + testKey},
		{"X-Api-Key": testKey},
	} {
		hdr["X-Internal-Token"] = "client-forged"
		hdr["X-Parsedock-Owner"] = "victim"
		hdr[HeaderClientIP] = "10.9.9.9"
		hdr["X-Debug-Owner"] = "victim"
		hdr["X-Request-Id"] = "req_abc"
		rec := call(h, "GET", "/v1/jobs/job_x", hdr)
		if rec.Code != 200 {
			t.Fatalf("authorised request: %d %s", rec.Code, rec.Body.String())
		}
		got := up.header()
		if got.Get("X-Internal-Token") != testInternal {
			t.Fatalf("internal token not injected: %q", got.Get("X-Internal-Token"))
		}
		if got.Get("X-Parsedock-Owner") != "acme" {
			t.Fatalf("owner = %q, want acme", got.Get("X-Parsedock-Owner"))
		}
		if got.Get("Authorization") != "" || got.Get("X-Api-Key") != "" || got.Get("X-Debug-Owner") != "" {
			t.Fatalf("client credential reached the coordinator: %v", got)
		}
		if got.Get(HeaderClientIP) != "192.0.2.10" || got.Get("X-Forwarded-For") != "192.0.2.10" {
			t.Fatalf("client ip = %q / %q", got.Get(HeaderClientIP), got.Get("X-Forwarded-For"))
		}
		if got.Get("X-Request-Id") != "req_abc" {
			t.Fatalf("request id = %q", got.Get("X-Request-Id"))
		}
	}
}

func TestAuthEnforcesScopes(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, nil)
	ro := map[string]string{"Authorization": "Bearer read-only-key-0123456789abcdef"}
	if rec := call(h, "GET", "/v1/jobs/job_x", ro); rec.Code != 200 {
		t.Fatalf("read with read scope: %d", rec.Code)
	}
	for _, p := range []string{"/v1/jobs/job_x/ack", "/v1/jobs/job_x/cancel", "/v1/jobs", "/v1/parse"} {
		if rec := call(h, "POST", p, ro); rec.Code != 403 || !strings.Contains(rec.Body.String(), "insufficient_scope") {
			t.Fatalf("POST %s with a read-only key: %d %s", p, rec.Code, rec.Body.String())
		}
	}
}

func TestNoKeysWithoutDevInsecureIsClosed(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, func(c *Config) { c.Keys = nil })
	if rec := call(h, "GET", "/v1/jobs/job_x", nil); rec.Code != 401 {
		t.Fatalf("no keys configured must be 401, got %d", rec.Code)
	}
}

// X-Debug-Owner (dev-insecure only) is bounded: a client cannot mint arbitrary owner
// ids, inject separators or grow the limiter map with junk.
func TestDebugOwnerIsValidated(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, func(c *Config) { c.Keys = nil; c.DevInsecure = true })
	for in, want := range map[string]string{
		"":                        "dev",
		"tenant-a":                "tenant-a",
		"bad owner":               "dev",
		strings.Repeat("a", 65):   "dev",
		strings.Repeat("b", 64):   strings.Repeat("b", 64),
		"evil\",\"audit\":\"true": "dev",
	} {
		if rec := call(h, "GET", "/v1/jobs/job_x", map[string]string{"X-Debug-Owner": in}); rec.Code != 200 {
			t.Fatalf("dev request: %d", rec.Code)
		}
		if got := up.header().Get("X-Parsedock-Owner"); got != want {
			t.Errorf("X-Debug-Owner %q -> owner %q, want %q", in, got, want)
		}
	}
}

// Probes are unauthenticated: the gateway must not attach the internal token (that
// would hand the coordinator's detailed readiness view to the public).
func TestReadyzIsForwardedWithoutInternalCredentials(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, nil)
	rec := call(h, "GET", "/readyz", map[string]string{"X-Internal-Token": testInternal, "X-Parsedock-Owner": "x"})
	if rec.Code != 200 {
		t.Fatalf("readyz: %d", rec.Code)
	}
	got := up.header()
	if got.Get("X-Internal-Token") != "" || got.Get("X-Parsedock-Owner") != "" {
		t.Fatalf("probe carried internal credentials: %v", got)
	}
	if call(h, "GET", "/healthz", nil).Code != 200 {
		t.Fatal("healthz")
	}
	if call(h, "GET", "/internal/metrics", nil).Code != 404 {
		t.Fatal("the gateway must not expose /internal")
	}
}

func TestOwnerRateLimit(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, func(c *Config) { c.RateLimitRPS = 0.001; c.RateBurst = 2 })
	auth := map[string]string{"Authorization": "Bearer " + testKey}
	for i := 0; i < 2; i++ {
		if rec := call(h, "GET", "/v1/jobs/job_x", auth); rec.Code != 200 {
			t.Fatalf("request %d within burst: %d", i, rec.Code)
		}
	}
	rec := call(h, "GET", "/v1/jobs/job_x", auth)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over the burst: %d %v", rec.Code, rec.Header())
	}
}

// --rate-limit=0 used to be silently reset to 20; it now disables the limiter, and a
// negative value is refused.
func TestRateLimitZeroDisablesNegativeRejected(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, func(c *Config) { c.RateLimitRPS = 0; c.RateBurst = 1 })
	auth := map[string]string{"Authorization": "Bearer " + testKey}
	for i := 0; i < 50; i++ {
		if rec := call(h, "GET", "/v1/jobs/job_x", auth); rec.Code != 200 {
			t.Fatalf("request %d with the limiter disabled: %d", i, rec.Code)
		}
	}
	if _, err := New(Config{CoordinatorURL: up.srv.URL, RateLimitRPS: -1, Log: quietLog()}); err == nil {
		t.Fatal("a negative rate limit must be a configuration error")
	}
}

// Failed authentication is limited per client address before the owner is known.
func TestFailedAuthIsRateLimitedPerIP(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, nil)
	bad := map[string]string{"Authorization": "Bearer wrong-token-0123456789"}
	saw429 := false
	for i := 0; i < authFailBurst+5; i++ {
		rec := call(h, "GET", "/v1/jobs/job_x", bad)
		if rec.Code == 429 {
			saw429 = true
			if !strings.Contains(rec.Body.String(), "rate_limited") || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("auth flood response: %s %v", rec.Body.String(), rec.Header())
			}
			break
		}
		if rec.Code != 401 {
			t.Fatalf("failure %d: %d", i, rec.Code)
		}
	}
	if !saw429 {
		t.Fatalf("%d failed attempts from one address were never throttled", authFailBurst+5)
	}
	// Another address is unaffected.
	req := httptest.NewRequest("GET", "/v1/jobs/job_x", nil)
	req.RemoteAddr = "198.51.100.7:1234"
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("an unrelated address was throttled: %d", rec.Code)
	}
}

func TestLimiterIsBounded(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(1, 5, 100)
	l.now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		l.allow("key-" + string(rune('a'+i%26)) + "-" + time.Duration(i).String())
		if l.size() > 100 {
			t.Fatalf("limiter grew to %d entries (max 100)", l.size())
		}
	}
	// Idle buckets (refilled to the burst) are evicted before busy ones.
	l2 := newLimiter(1, 2, 3)
	l2.now = func() time.Time { return now }
	l2.allow("busy")
	l2.allow("busy")
	l2.allow("idle-1")
	l2.allow("idle-2")
	now = now.Add(time.Second) // idle-1/idle-2 refill to 2; busy refills to 1
	l2.allow("new")
	if _, ok := l2.bucket["busy"]; !ok {
		t.Fatal("a busy bucket was evicted while idle ones were available")
	}
	// blocked() never creates entries.
	before := l2.size()
	if l2.blocked("unknown") || l2.size() != before {
		t.Fatal("blocked() must not create a bucket")
	}
}

func TestClientIPAndTrustedProxies(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8, 192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrustedProxies("not-a-cidr"); err == nil {
		t.Fatal("garbage must be rejected")
	}
	cases := []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		xff     string
		want    string
	}{
		{"no trusted proxies ignores XFF", nil, "192.0.2.10:1", "203.0.113.5", "192.0.2.10"},
		{"trusted peer, client from XFF", trusted, "192.0.2.10:1", "203.0.113.5", "203.0.113.5"},
		{"spoofed left part ignored", trusted, "192.0.2.10:1", "1.1.1.1, 203.0.113.5, 10.1.2.3", "203.0.113.5"},
		{"untrusted peer ignores XFF", trusted, "198.51.100.1:1", "203.0.113.5", "198.51.100.1"},
		{"malformed hop stops the walk", trusted, "192.0.2.10:1", "203.0.113.5, garbage", "192.0.2.10"},
		{"ipv4-mapped peer", nil, "[::ffff:198.51.100.2]:1", "", "198.51.100.2"},
	}
	for _, c := range cases {
		s := &Server{cfg: Config{TrustedProxies: c.trusted}}
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientIP(req); got != c.want {
			t.Errorf("%s: clientIP = %q, want %q", c.name, got, c.want)
		}
	}
}

// Finding: the shared 30 s ResponseHeaderTimeout cut every sync parse longer than 30 s
// off with a 502. The gateway transport must outlast the 120 s sync window.
func TestProxyTransportOutlastsSyncParse(t *testing.T) {
	if DefaultProxyResponseHeaderTimeout < syncParseMaxWait+10*time.Second {
		t.Fatalf("default proxy header timeout %s does not cover the %s sync window",
			DefaultProxyResponseHeaderTimeout, syncParseMaxWait)
	}
	up := newUpstream(t, nil)
	gw, err := New(Config{CoordinatorURL: up.srv.URL, Log: quietLog(), RateLimitRPS: 1})
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := gw.proxy.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport %T", gw.proxy.Transport)
	}
	if tr.ResponseHeaderTimeout != DefaultProxyResponseHeaderTimeout {
		t.Fatalf("proxy header timeout = %s", tr.ResponseHeaderTimeout)
	}

	// Behaviour, with short values so the test is fast: an upstream slower than the
	// worker-style timeout still succeeds when the proxy timeout is longer.
	slow := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	})
	auth := map[string]string{"Authorization": "Bearer " + testKey}
	short := newGateway(t, slow, func(c *Config) { c.ProxyResponseHeaderTimeout = 50 * time.Millisecond })
	if rec := call(short, "POST", "/v1/parse", auth); rec.Code != 503 {
		t.Fatalf("a header timeout shorter than the upstream must answer coordinator_unavailable (503), got %d", rec.Code)
	}
	long := newGateway(t, slow, func(c *Config) { c.ProxyResponseHeaderTimeout = 2 * time.Second })
	if rec := call(long, "POST", "/v1/parse", auth); rec.Code != 200 {
		t.Fatalf("a slow sync parse within the proxy timeout must succeed, got %d", rec.Code)
	}
}

func TestEdgeHeadersAndRequestID(t *testing.T) {
	up := newUpstream(t, nil)
	h := newGateway(t, up, nil)
	rec := call(h, "GET", "/healthz", map[string]string{"X-Request-Id": "bad id\r\nx"})
	id := rec.Header().Get("X-Request-Id")
	if !strings.HasPrefix(id, "req_") {
		t.Fatalf("invalid request id was not replaced: %q", id)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["role"] != "api" {
		t.Fatalf("healthz body: %v", body)
	}
}

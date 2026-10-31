package coordinator

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	gwToken     = "gateway-token-0123456789abcdef"
	workerToken = "worker-token-0123456789abcdef"
)

// testMux mirrors the coordinator listener wiring in cmd/docparse.
func testMux(t *testing.T, internal, worker string) (*Server, http.Handler) {
	t.Helper()
	svc := testService(t, nil)
	srv := NewServer(svc, internal, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.WorkerToken = worker
	mux := http.NewServeMux()
	mux.Handle("/internal/", srv.InternalMux())
	mux.Handle("/v1/", srv.PublicMux())
	mux.Handle("/readyz", srv.PublicMux())
	return srv, mux
}

func do(h http.Handler, method, path, token string, hdr map[string]string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Finding: a single shared token let any worker assert X-Parsedock-Owner and read or
// ACK every tenant's results. With a worker token configured, the worker credential
// opens the worker protocol only.
func TestWorkerTokenCannotActForOwners(t *testing.T) {
	_, h := testMux(t, gwToken, workerToken)
	owner := map[string]string{"X-Parsedock-Owner": "acme"}
	jobPath := "/v1/jobs/job_06GRQJT5HC1DDZMPWRSKSCKM24"

	cases := []struct {
		name, method, path, token string
		hdr                       map[string]string
		body                      string
		want                      int
	}{
		{"worker token on /v1 read", "GET", jobPath, workerToken, owner, "", 403},
		{"worker token on /v1 ack", "POST", jobPath + "/ack", workerToken, owner, `{}`, 403},
		{"worker token on /v1/parse", "POST", "/v1/parse", workerToken, owner, "", 403},
		{"worker token on metrics", "GET", "/internal/metrics", workerToken, nil, "", 403},
		{"worker token on diagnostics", "GET", "/internal/diagnostics/workers", workerToken, nil, "", 403},
		{"worker token on throttle", "POST", "/internal/throttle", workerToken, nil, `{"counts":{}}`, 403},
		{"wrong token on /v1", "GET", jobPath, "nope", owner, "", 401},
		{"gateway token on /v1", "GET", jobPath, gwToken, owner, "", 404},
		{"gateway token on metrics", "GET", "/internal/metrics", gwToken, nil, "", 200},
		{"gateway token on diagnostics", "GET", "/internal/diagnostics/workers", gwToken, nil, "", 200},
		{"worker token claims", "POST", "/internal/tasks/claim", workerToken, nil, `{"worker_id":"w1","engines":["native"],"device":"cpu"}`, 204},
		{"worker token registers", "POST", "/internal/workers/register", workerToken, nil, `{"worker_id":"w1","engines":["native"],"device_type":"cpu"}`, 200},
		// Compatibility: deployments that still give workers the internal token keep working.
		{"internal token claims", "POST", "/internal/tasks/claim", gwToken, nil, `{"worker_id":"w1","engines":["native"],"device":"cpu"}`, 204},
		{"wrong token claims", "POST", "/internal/tasks/claim", "nope", nil, `{}`, 401},
		{"no token claims", "POST", "/internal/tasks/claim", "", nil, `{}`, 401},
	}
	for _, c := range cases {
		rec := do(h, c.method, c.path, c.token, c.hdr, strings.NewReader(c.body))
		if rec.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d (%s)", c.name, c.method, c.path, rec.Code, c.want, rec.Body.String())
		}
	}
}

// Without a worker token the single-token behaviour is unchanged.
func TestSingleTokenBehaviourUnchanged(t *testing.T) {
	_, h := testMux(t, gwToken, "")
	owner := map[string]string{"X-Parsedock-Owner": "acme"}
	for _, c := range []struct {
		method, path, token string
		hdr                 map[string]string
		body                string
		want                int
	}{
		{"GET", "/v1/jobs/job_06GRQJT5HC1DDZMPWRSKSCKM24", gwToken, owner, "", 404},
		{"POST", "/internal/tasks/claim", gwToken, nil, `{"worker_id":"w1","engines":["native"],"device":"cpu"}`, 204},
		{"GET", "/internal/diagnostics/workers", gwToken, nil, "", 200},
		{"GET", "/internal/metrics", "", nil, "", 401},
		{"POST", "/internal/tasks/claim", workerToken, nil, `{}`, 401},
	} {
		if rec := do(h, c.method, c.path, c.token, c.hdr, strings.NewReader(c.body)); rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}

func TestValidateTokens(t *testing.T) {
	if err := ValidateTokens("", ""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTokens(gwToken, ""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTokens(gwToken, workerToken); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTokens("", workerToken); err == nil {
		t.Fatal("a worker token without an internal token must be refused")
	}
	if err := ValidateTokens(gwToken, gwToken); err == nil {
		t.Fatal("identical tokens must be refused")
	}
}

// Finding: /readyz is unauthenticated and leaked free bytes, the last cleanup time and
// raw error strings with paths. The public view keeps only verdicts.
func TestReadyzRedactsDetailsWithoutInternalToken(t *testing.T) {
	_, h := testMux(t, gwToken, workerToken)
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		var body struct {
			Status string         `json:"status"`
			Checks map[string]any `json:"checks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("readyz body: %v (%s)", err, rec.Body.String())
		}
		if body.Status != "ready" {
			t.Fatalf("status = %q", body.Status)
		}
		return body.Checks
	}
	for _, token := range []string{"", workerToken, "nope"} {
		rec := do(h, "GET", "/readyz", token, nil, nil)
		if rec.Code != 200 {
			t.Fatalf("readyz = %d", rec.Code)
		}
		checks := decode(rec)
		for _, leaked := range []string{"free_bytes", "last_cleanup", "sqlite_error", "storage_error"} {
			if _, ok := checks[leaked]; ok {
				t.Errorf("public readyz (token %q) exposes %s: %v", token, leaked, checks)
			}
		}
		for _, want := range []string{"sqlite", "storage", "cleanup", "accepting_uploads"} {
			if _, ok := checks[want]; !ok {
				t.Errorf("public readyz lost the %s verdict: %v", want, checks)
			}
		}
	}
	checks := decode(do(h, "GET", "/readyz", gwToken, nil, nil))
	for _, want := range []string{"free_bytes", "last_cleanup"} {
		if _, ok := checks[want]; !ok {
			t.Errorf("operator readyz lost %s: %v", want, checks)
		}
	}
}

// Finding: the client address was lost after the gateway. The coordinator now records
// the gateway-resolved address in audit lines, and only a valid IP literal.
func TestAuditCarriesGatewayClientIP(t *testing.T) {
	for _, c := range []struct{ header, want string }{
		{"203.0.113.9", `"client_ip":"203.0.113.9"`},
		{"::ffff:198.51.100.4", `"client_ip":"198.51.100.4"`},
		{"1.2.3.4\nfake=1", ""},
		{"", ""},
	} {
		srv, h := testMux(t, gwToken, workerToken)
		var buf bytes.Buffer
		srv.Svc.Audit = slog.New(slog.NewJSONHandler(&buf, nil))

		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "a.txt")
		_, _ = fw.Write([]byte("hello"))
		_ = mw.Close()
		hdr := map[string]string{
			"X-Parsedock-Owner": "acme",
			"Content-Type":      mw.FormDataContentType(),
		}
		if c.header != "" {
			hdr[clientIPHeader] = c.header
		}
		rec := do(h, "POST", "/v1/jobs", gwToken, hdr, &body)
		if rec.Code != 202 {
			t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
		}
		out := buf.String()
		if !strings.Contains(out, `"event":"job_created"`) {
			t.Fatalf("no audit line: %s", out)
		}
		if c.want != "" && !strings.Contains(out, c.want) {
			t.Errorf("header %q: audit lacks %s: %s", c.header, c.want, out)
		}
		if c.want == "" && strings.Contains(out, "client_ip") {
			t.Errorf("header %q must not produce a client_ip: %s", c.header, out)
		}
	}
}
